package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/url"
	"os"
	"time"

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
	peerAddrFlag      string
	databaseFlag      string
	writeToInfluxFlag bool
)

func init() {
	flag.StringVar(&peerAddrFlag, "peer", "", "rendezvous peer address: 0xEth@ip:port")
	flag.StringVar(&databaseFlag, "db", "geo.mmdb", "path to geoip database")
	flag.BoolVar(&writeToInfluxFlag, "write", false, "write data to influx")

	flag.Parse()
}

func main() {
	if len(peerAddrFlag) == 0 {
		log.Println("endpoint is empty, exiting")
		os.Exit(1)
	}

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

	addr, err := auth.ParseAddr(peerAddrFlag)
	if err != nil {
		log.Printf("cannot parse string `%s` into peer endpoint: %v\n", peerAddrFlag, err)
		os.Exit(1)
	}

	eth, err := addr.ETH()
	if err != nil {
		log.Printf("cannot extract eth part from addr `%s`: %v\n", peerAddrFlag, err)
		os.Exit(1)
	}

	ip, err := addr.Addr()
	if err != nil {
		log.Printf("cannot extract IP part from addr `%s`: %v\n", peerAddrFlag, err)
		os.Exit(1)
	}

	creds := auth.NewWalletAuthenticator(util.NewTLS(TLSConfig), eth)
	client, err := xgrpc.NewClient(ctx, ip, creds)
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
				continue
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
		log.Printf("geohash=%s    name=%s    count=%d\n", hash, names[hash], counter)
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
		log.Printf("cannot write influx point: %v\n", err)
		os.Exit(1)
	}
}

func getInfluxClient() *influx.Client {
	u, err := url.Parse("http://127.0.0.1:8086")
	if err != nil {
		log.Printf("cannot parse string into url: %v\n", err)
		os.Exit(1)
	}

	client, err := influx.NewClient(influx.Config{URL: *u})
	if err != nil {
		log.Printf("cannot create influx client: %v\n", err)
		os.Exit(1)
	}

	return client
}
