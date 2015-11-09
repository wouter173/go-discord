package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gdraynz/go-discord/discord"
)

var (
	flagConf   = flag.String("conf", "conf.json", "Configuration file")
	flagPlayed = flag.String("played", "played.json", "Played time dump json file")
	flagStdout = flag.Bool("stdout", true, "Logs to stdout")

	client        discord.Client
	startTime     time.Time
	commands      map[string]Command
	games         map[int]discord.Game
	totalCommands int

	// Map containing currently playing users
	usersPlaying map[string]chan bool

	// Store playtime as nanoseconds
	playedTime map[string]map[string]int64
)

type Command struct {
	Word    string
	Help    string
	Handler func(discord.Message, ...string)
}

func loadPlayedTime() error {
	playedTime = make(map[string]map[string]int64)

	_, err := os.Stat(*flagPlayed)
	if err != nil {
		_, err := os.OpenFile(*flagPlayed, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
	} else {
		dump, err := ioutil.ReadFile(*flagPlayed)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(dump, &playedTime); err != nil {
			return err
		}
	}

	return nil
}

func savePlayedTime() {
	for _, c := range usersPlaying {
		c <- true
	}

	// Wait 10ms to save all play times (purely speculative)
	time.Sleep(10 * time.Millisecond)

	dump, err := json.Marshal(playedTime)
	if err != nil {
		log.Print(err)
		return
	}
	if err := ioutil.WriteFile(*flagPlayed, dump, 0600); err != nil {
		log.Print(err)
	}
}

func onReady(ready discord.Ready) {
	startTime = time.Now()
	usersPlaying = make(map[string]chan bool)
	totalCommands = 0

	// Init game list
	var err error
	games, err = discord.GetGamesFromFile("games.json")
	if err != nil {
		log.Print("err: Failed to load games")
	}

	// Start playtime count for everyone already playing
	for _, server := range ready.Servers {
		for _, presence := range server.Presences {
			go gameStarted(presence)
		}
	}
}

func messageReceived(message discord.Message) {
	if !strings.HasPrefix(message.Content, "!go") {
		return
	}

	args := strings.Split(message.Content, " ")
	if len(args)-1 < 1 {
		return
	}

	totalCommands++

	command, ok := commands[args[1]]
	if ok {
		command.Handler(message, args...)
	} else {
		log.Printf("No command '%s'", args[1])
	}
}

func countPlaytime(user discord.User, game discord.Game) {
	log.Printf("Starting to count for %s on %s", user.Name, game.Name)

	usersPlaying[user.ID] = make(chan bool)
	start := time.Now()
	_, ok := playedTime[user.ID]
	if !ok {
		playedTime[user.ID] = make(map[string]int64)
	}

	// Wait for game to end
	<-usersPlaying[user.ID]

	// Delete user from playing list
	delete(usersPlaying, user.ID)

	// Update player's game time
	strGameID := strconv.Itoa(game.ID)
	_, alreadyPlayed := playedTime[user.ID][strGameID]
	if alreadyPlayed {
		total := time.Now().Add(time.Duration(playedTime[user.ID][strGameID]))
		playedTime[user.ID][strGameID] = total.Sub(start).Nanoseconds()
	} else {
		playedTime[user.ID][strGameID] = time.Since(start).Nanoseconds()
	}

	log.Printf("Done counting for %s", user.Name)
}

func gameStarted(presence discord.Presence) {
	user := presence.GetUser(&client)
	game, exists := games[presence.GameID]
	c, ok := usersPlaying[user.ID]

	if ok && !exists {
		c <- true
	} else if ok && exists {
		log.Printf("Ignoring multiple presence from '%s'", user.Name)
	} else if exists {
		countPlaytime(user, game)
	}
}

func getDurationString(duration time.Duration) string {
	return fmt.Sprintf(
		"%0.2d:%02d:%02d",
		int(duration.Hours()),
		int(duration.Minutes())%60,
		int(duration.Seconds())%60,
	)
}

func getUserCountString() string {
	users := 0
	channels := 0
	for _, server := range client.Servers {
		users += len(server.Members)
		channels += len(server.Channels)
	}
	return fmt.Sprintf(
		"%d in %d channels and %d servers",
		users,
		channels,
		len(client.Servers),
	)
}

func statsCommand(message discord.Message, args ...string) {
	stats := runtime.MemStats{}
	runtime.ReadMemStats(&stats)
	client.SendMessage(
		message.ChannelID,
		fmt.Sprintf("Bot statistics:\n"+
			"`Memory used` %.2f Mb\n"+
			"`Users in touch` %s\n"+
			"`Uptime` %s\n"+
			"`Concurrent tasks` %d\n"+
			"`Commands answered` %d",
			float64(stats.Alloc)/1000000,
			getUserCountString(),
			getDurationString(time.Now().Sub(startTime)),
			runtime.NumGoroutine(),
			totalCommands,
		),
	)
}

func helpCommand(message discord.Message, args ...string) {
	toSend := "Available commands:\n"
	for _, command := range commands {
		toSend += fmt.Sprintf("`%s` %s\n", command.Word, command.Help)
	}
	client.SendMessage(message.ChannelID, toSend)
}

func reminderCommand(message discord.Message, args ...string) {
	if len(args)-1 < 2 {
		return
	}

	duration, err := time.ParseDuration(args[2])
	if err != nil {
		client.SendMessage(
			message.ChannelID,
			fmt.Sprintf("Couldn't understand that :("),
		)
	} else {
		var reminderMessage string
		if len(args)-1 < 3 {
			reminderMessage = fmt.Sprintf("@%s ping !", message.Author.Name)
		} else {
			reminderMessage = fmt.Sprintf(
				"@%s %s !",
				message.Author.Name,
				strings.Join(args[3:], " "),
			)
		}
		client.SendMessage(
			message.ChannelID,
			fmt.Sprintf("Aight! I will ping you in %s.", duration.String()),
		)
		log.Printf("Reminding %s in %s", message.Author.Name, duration.String())
		time.AfterFunc(duration, func() {
			client.SendMessageMention(
				message.ChannelID,
				reminderMessage,
				[]discord.User{message.Author},
			)
		})
	}
}

func sourceCommand(message discord.Message, args ...string) {
	client.SendMessage(message.ChannelID, "https://github.com/gdraynz/go-discord")
}

func avatarCommand(message discord.Message, args ...string) {
	client.SendMessage(message.ChannelID, message.Author.GetAvatarURL())
}

func voiceCommand(message discord.Message, args ...string) {
	if message.Author.Name != "steelou" {
		client.SendMessage(message.ChannelID, "Nah.")
		return
	}

	server := message.GetServer(&client)
	voiceChannel := client.GetChannel(server, "General")
	if err := client.SendAudio(voiceChannel, "Blue.mp3"); err != nil {
		log.Print(err)
	}
}

func playedCommand(message discord.Message, args ...string) {
	var pString string
	if len(playedTime[message.Author.ID]) == 0 {
		pString = "Seems you played nothing since I'm up :("
	} else {
		pString = "As far as I'm aware, you played:\n"
		for strGameID, playtime := range playedTime[message.Author.ID] {
			id, err := strconv.Atoi(strGameID)
			if err != nil {
				client.SendMessage(message.ChannelID, "Seems like I just broke. :|")
				return
			}
			pString += fmt.Sprintf(
				"`%s` %s\n",
				games[id].Name,
				getDurationString(time.Duration(playtime)),
			)
		}
	}
	client.SendMessage(message.ChannelID, pString)
}

func main() {
	flag.Parse()

	if err := loadPlayedTime(); err != nil {
		log.Print(err)
	}

	// Logging
	var logfile *os.File
	if !*flagStdout {
		var err error
		logfile, err = os.OpenFile("gobot.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0600)
		if err != nil {
			log.Fatal(err)
		}
		log.SetOutput(logfile)
	}

	client = discord.Client{
		OnReady:          onReady,
		OnMessageCreate:  messageReceived,
		OnPresenceUpdate: gameStarted,

		// Debug: true,
	}

	commands = map[string]Command{
		"help": Command{
			Word:    "help",
			Help:    "Prints the help message",
			Handler: helpCommand,
		},
		"reminder": Command{
			Word:    "reminder <time [XhYmZs]> [<message>]",
			Help:    "Reminds you of something in X hours Y minutes Z seconds",
			Handler: reminderCommand,
		},
		"stats": Command{
			Word:    "stats",
			Help:    "Prints bot statistics",
			Handler: statsCommand,
		},
		"source": Command{
			Word:    "source",
			Help:    "Shows the bot's source URL",
			Handler: sourceCommand,
		},
		"avatar": Command{
			Word:    "avatar",
			Help:    "Shows your avatar URL",
			Handler: avatarCommand,
		},
		"played": Command{
			Word:    "played",
			Help:    "Shows your play time",
			Handler: playedCommand,
		},
		// "watch": Command{
		// 	Word:    "watch <user> [<game>]",
		// 	Help:    "Ping you when <user> starts to play <game>",
		// 	Handler: watchCommand,
		// },
		// "unwatch": Command{
		// 	Word:    "unwatch <user> [<game>]",
		// 	Help:    "Stop notifying from the watch command",
		// 	Handler: unwatchCommand,
		// },
	}

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, os.Kill, syscall.SIGTERM)
	go func(c chan os.Signal) {
		sig := <-c
		log.Printf("Caught signal %s: shutting down", sig)
		savePlayedTime()
		if logfile != nil {
			logfile.Close()
		}
		client.Stop()
		os.Exit(0)
	}(sigc)

	if err := client.LoginFromFile(*flagConf); err != nil {
		log.Fatal(err)
	}

	client.Run()
}
