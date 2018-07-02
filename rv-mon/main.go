package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/mmcloughlin/geohash"
	"github.com/oschwald/geoip2-golang"
	"github.com/sonm-io/core/insonmnia/auth"
	"github.com/sonm-io/core/proto"
	"github.com/sonm-io/core/util"
	"github.com/sonm-io/core/util/xgrpc"

	influx "github.com/influxdata/influxdb/client"
)

var (
	endpointFlag      string
	peerAddrFlag      string
	databaseFlag      string
	debugLogPath      string
	writeToInfluxFlag bool
)

func init() {
	flag.StringVar(&endpointFlag, "endpoint", "", "relay monitoring endpoint")
	flag.StringVar(&peerAddrFlag, "peer", "0x1243742340d5504d88af3360036ec9019b933164", "relay peer address")
	flag.StringVar(&databaseFlag, "db", "geo.mmdb", "path to geoip database")
	flag.BoolVar(&writeToInfluxFlag, "write", false, "write data to influx")
	flag.StringVar(&debugLogPath, "debigLog", "/tmp/rv_mon.log", "file to write debug info")

	flag.Parse()

	// init logger
	logFile, err := os.OpenFile(debugLogPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open file for wrinig: %v\n", err)
		os.Exit(1)
	}

	defer logFile.Close()
	log.SetOutput(logFile)
}

type point struct {
	geohash string
	name    string
	eth     string
}

func main() {
	if len(endpointFlag) == 0 {
		fmt.Fprintln(os.Stderr, "endpoint is empty, exiting")
		os.Exit(1)
	}

	// init logger
	logFile, err := os.OpenFile(debugLogPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open file for wrinig: %v\n", err)
		os.Exit(1)
	}

	defer logFile.Close()
	log.SetOutput(logFile)

	key, err := crypto.GenerateKey()
	if err != nil {
		log.Printf("cannot generate key: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	_, TLSConfig, err := util.NewHitlessCertRotator(ctx, key)
	if err != nil {
		log.Printf("cannot create TLS config: %v\n", err)
		os.Exit(1)
	}

	creds := auth.NewWalletAuthenticator(util.NewTLS(TLSConfig), common.HexToAddress(peerAddrFlag))
	client, err := xgrpc.NewClient(ctx, endpointFlag, creds)
	if err != nil {
		log.Printf("cannot create client connection: %v\n", err)
		os.Exit(1)
	}

	rv := sonm.NewRendezvousClient(client)
	info, err := rv.Info(ctx, &sonm.Empty{})
	if err != nil {
		log.Printf("cannot query rv clients: %v\n", err)
		os.Exit(1)
	}

	db, err := geoip2.Open(databaseFlag)
	if err != nil {
		log.Printf("cannot open geoip db: %v\n", err)
		os.Exit(1)
	}

	defer db.Close()

	var pointCounters = map[string]int{}
	var nameCache = map[string]string{}

	for _, state := range info.GetState() {
		for _, srv := range state.GetServers() {
			ip := net.ParseIP(srv.PublicAddr.Addr.Addr)
			rec, err := db.City(ip)
			if err != nil {
				log.Printf("cannot find IP `%s` in geoip db: %v\n", ip.String(), err)
			}

			pointEncoded := geohash.Encode(rec.Location.Latitude, rec.Location.Longitude)
			var name string
			if len(rec.City.Names["en"]) > 0 {
				name = rec.City.Names["en"]
			} else {
				name = rec.Country.Names["en"]
			}

			nameCache[pointEncoded] = name
			if _, ok := pointCounters[pointEncoded]; ok {
				pointCounters[pointEncoded] += 1
			} else {
				pointCounters[pointEncoded] = 1
			}
		}
	}

	if writeToInfluxFlag {
		writeToInflux(pointCounters, nameCache)
	} else {
		writeToConsole(pointCounters, nameCache)
	}
}

func writeToConsole(points map[string]int, names map[string]string) {
	for hash, counter := range points {
		fmt.Printf("geohash=%s    name=%s    count=%d\n", hash, names[hash], counter)
	}
}

func writeToInflux(points map[string]int, names map[string]string) {
	var infPoints []influx.Point

	for hash, counter := range points {
		infPoints = append(infPoints, influx.Point{
			Measurement: "map_data",
			Fields: map[string]interface{}{
				"geohash": hash,
				"name":    names[hash],
				"count":   counter,
			},
			Precision: "s",
		})
	}

	pb := influx.BatchPoints{
		Database:  "telegraf",
		Precision: "s",
		Points:    infPoints,
	}

	infc := getInfluxClient()
	_, err := infc.Write(pb)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot write influx point: %v\n", err)
		os.Exit(1)
	}
}

func getInfluxClient() *influx.Client {
	u, err := url.Parse("http://127.0.0.1:8086")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot parse url O_o: %v\n", err)
		os.Exit(1)
	}

	client, err := influx.NewClient(influx.Config{URL: *u})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot create influx client: %v\n", err)
		os.Exit(1)
	}

	return client
}
