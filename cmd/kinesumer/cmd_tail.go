package main

import (
	"io"
	"os"

	"github.com/codegangsta/cli"
	"github.com/fatih/color"
	"github.com/remind101/kinesumer"
	"github.com/remind101/kinesumer/checkpointers/redis"
	"github.com/remind101/kinesumer/redispool"
)

var cmdTail = cli.Command{
	Name:    "tail",
	Aliases: []string{"t"},
	Usage:   "Pipes a Kinesis stream to standard out",
	Action:  runTail,
	Flags: append(
		[]cli.Flag{
			cli.StringFlag{
				Name:  "stream, s",
				Usage: "The Kinesis stream to tail",
			},
			cli.StringFlag{
				Name:  "duration, d",
				Usage: "Duration to go back and stream logs from",
			},
		}, flagsRedis...,
	),
}

func errHandler(err kinesumer.IError) {
	switch err.Severity() {
	case kinesumer.ECrit:
		fallthrough
	case kinesumer.EError:
		color.Red("%s:%s\n", err.Severity(), err.Error())
		panic(err)
	default:
		color.Yellow("%s:%s\n", err.Severity(), err.Error())
	}
}

func runTail(ctx *cli.Context) {
	k, err := kinesumer.NewDefault(
		ctx.String("stream"),
		ctx.String("duration"),
	)
	if err != nil {
		panic(err)
	}

	k.Options.ErrHandler = kinesumer.ErrHandler(errHandler)

	if redisURL := ctx.String(fRedisURL); len(redisURL) > 0 {
		pool, err := redispool.NewRedisPool(redisURL)
		if err != nil {
			panic(err)
		}

		cp, err := redischeckpointer.New(&redischeckpointer.Options{
			ReadOnly:    true,
			RedisPool:   pool,
			RedisPrefix: ctx.String(fRedisPrefix),
		})
		if err != nil {
			panic(err)
		}

		k.Checkpointer = cp
	}

	_, err = k.Begin()
	if err != nil {
		panic(err)
	}
	defer k.End()

	r := kinesumer.NewReader(k.Records())
	io.Copy(os.Stdout, r)
}
