package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"log"
	"time"

	"github.com/boltdb/bolt"
	"github.com/gdraynz/go-discord/discord"
)

var (
	flagDB = flag.String("db", "gametime.db", "DB file for game time")
)

type PlayingUser struct {
	UserID    string
	StartTime time.Time
	GameID    string
}

func (p *PlayingUser) SaveGametime(t *bolt.Tx) error {
	b, err := t.CreateBucketIfNotExists([]byte(p.UserID))
	if err != nil {
		return err
	}
	bPlayed := b.Get([]byte(p.GameID))
	if bPlayed != nil {
		// bytes to int64
		played, _ := binary.Varint(bPlayed)

		// Calc total time
		total := time.Now().Add(time.Duration(played))

		// int64 to bytes
		newPlayed := make([]byte, binary.MaxVarintLen64)
		binary.PutVarint(newPlayed, total.Sub(p.StartTime).Nanoseconds())

		if err := b.Put([]byte(p.GameID), newPlayed); err != nil {
			return err
		}
	} else {
		// int64 to bytes
		newPlayed := make([]byte, binary.MaxVarintLen64)
		binary.PutVarint(newPlayed, time.Since(p.StartTime).Nanoseconds())

		if err := b.Put([]byte(p.GameID), newPlayed); err != nil {
			return err
		}
	}
	// Update start time
	p.StartTime = time.Now()
	return nil
}

type TimeCounter struct {
	InProgress   map[string]PlayingUser
	GametimeDB   *bolt.DB
	GametimeChan chan PlayingUser
}

func NewCounter() (*TimeCounter, error) {
	var t *TimeCounter

	db, err := bolt.Open(*flagDB, 0600, nil)
	if err != nil {
		return t, err
	}

	return &TimeCounter{
		InProgress:   make(map[string]PlayingUser),
		GametimeDB:   db,
		GametimeChan: make(chan PlayingUser),
	}, nil
}

func (counter *TimeCounter) Listen() {
	for {
		pUser := <-counter.GametimeChan
		go counter.EndGametime(pUser)
	}
}

func (counter *TimeCounter) StartGametime(user discord.User, game discord.Game) {
	log.Printf("Starting to count for %s on %s", user.Name, game.Name)

	pUser := PlayingUser{
		UserID:    user.ID,
		GameID:    string(game.ID),
		StartTime: time.Now(),
	}

	counter.InProgress[user.ID] = pUser
}

func (counter *TimeCounter) EndGametime(pUser PlayingUser) {
	// Delete user from playing list
	delete(counter.InProgress, pUser.UserID)

	// Update game time
	err := counter.GametimeDB.Update(func(t *bolt.Tx) error {
		err := pUser.SaveGametime(t)
		return err
	})

	if err != nil {
		log.Printf("Error while updating game time : %s", err.Error())
	}

	log.Printf("Saved %s", pUser.UserID)
}

func (counter *TimeCounter) GetUserGametime(user discord.User) (map[string]int64, error) {
	gameMap := make(map[string]int64)
	err := counter.GametimeDB.View(func(t *bolt.Tx) error {
		b := t.Bucket([]byte(user.ID))
		if b == nil {
			return errors.New("user never played")
		}
		// Iterate through all games
		b.ForEach(func(gameID []byte, nanoTime []byte) error {
			gameMap[string(gameID[:])], _ = binary.Varint(nanoTime)
			return nil
		})
		return nil
	})
	return gameMap, err
}

func (counter *TimeCounter) Snapshot() error {
	return counter.GametimeDB.Update(func(t *bolt.Tx) (err error) {
		for _, pUser := range counter.InProgress {
			err = pUser.SaveGametime(t)
			if err != nil {
				log.Print(err)
				continue
			}
		}
		log.Print("Snapshot done")
		return nil
	})
}

func (counter *TimeCounter) Close() {
	// Save times for currently playing users
	if err := counter.Snapshot(); err != nil {
		log.Print(err)
	}
	counter.GametimeDB.Close()
}