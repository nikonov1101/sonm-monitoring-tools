package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/oschwald/geoip2-golang"
	"github.com/sonm-io/core/insonmnia/auth"
	"github.com/sonm-io/core/proto"
	"github.com/sonm-io/core/util"
	"github.com/sonm-io/core/util/xgrpc"

	_ "net/http/pprof"
)

const (
	rvAddr     = "rendezvous.livenet.sonm.com:14099"
	rvEth      = "0x5b7d6516fad04e10db726933bcd75447fd7b4b17"
	dwhAddr    = "dwh.livenet.sonm.com:15021"
	dwhEth     = "0xadffcac607a0a1b583c489977eae413a62d4bc73"
	listedAddr = ":8090"

	pprofPrefix = "/debug/pprof"
)

var (
	databasePath string
	dwh          sonm.DWHClient
	rv           sonm.RendezvousClient
	db           *geoip2.Reader
)

func init() {
	flag.StringVar(&databasePath, "db", "geo.mmdb", "path to geoip database")
	flag.Parse()
}

type PeerPoint struct {
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Count    int     `json:"count"`
	Income   float64 `json:"income"`
	CPUCount uint64  `json:"cpu_count"`
	GPUCount uint64  `json:"gpu_count"`
	RAMSize  uint64  `json:"ram_size"`
}

type cache struct {
	mu    sync.Mutex
	green map[string]PeerPoint
	blue  map[string]PeerPoint
}

func (c *cache) update(peers map[string]PeerPoint) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.green == nil {
		c.green = peers
		c.blue = nil
	} else {
		c.blue = peers
		c.green = nil
	}
}

func (c *cache) get() map[string]PeerPoint {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.green != nil {
		return c.green
	} else {
		return c.blue
	}
}

func initConnections(ctx context.Context) {
	key, err := crypto.GenerateKey()
	if err != nil {
		log.Printf("cannot generate key: %v\n", err)
		os.Exit(1)
	}

	_, TLSConfig, err := util.NewHitlessCertRotator(ctx, key)
	if err != nil {
		log.Printf("cannot create TLS config: %v\n", err)
		os.Exit(1)
	}

	rvCreds := auth.NewWalletAuthenticator(util.NewTLS(TLSConfig), common.HexToAddress(rvEth))
	rvClient, err := xgrpc.NewClient(ctx, rvAddr, rvCreds)
	if err != nil {
		log.Printf("cannot create client connection (rv): %v\n", err)
		os.Exit(1)
	}

	dwhCreds := auth.NewWalletAuthenticator(util.NewTLS(TLSConfig), common.HexToAddress(dwhEth))
	dwhClient, err := xgrpc.NewClient(ctx, dwhAddr, dwhCreds)
	if err != nil {
		log.Printf("cannot create client connection (dwh): %v\n", err)
		os.Exit(1)
	}

	dwh = sonm.NewDWHClient(dwhClient)
	rv = sonm.NewRendezvousClient(rvClient)

	db, err = geoip2.Open(databasePath)
	if err != nil {
		log.Printf("cannot open geoip db: %v\n", err)
		os.Exit(1)
	}
}

func loadDeals(ctx context.Context, dwh sonm.DWHClient, addr common.Address) (PeerPoint, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	deals, err := dwh.GetDeals(ctx, &sonm.DealsRequest{
		Status:     sonm.DealStatus_DEAL_ACCEPTED,
		SupplierID: sonm.NewEthAddress(addr),
		// MasterID:         nil,
		// AnyUserID:        nil,
		Limit: 100,
	})

	if err != nil {
		return PeerPoint{}, err
	}

	p := PeerPoint{}
	income := big.NewInt(0)
	log.Printf("got %d deals for peer %s\n", len(deals.GetDeals()), addr.Hex())
	for _, deal := range deals.GetDeals() {
		p.Count += 1
		p.CPUCount += deal.GetDeal().Benchmarks.CPUCores()
		p.GPUCount += deal.GetDeal().GetBenchmarks().GPUCount()
		p.RAMSize = deal.GetDeal().GetBenchmarks().RAMSize()
		income = big.NewInt(0).Add(income, deal.GetDeal().GetPrice().Unwrap())
	}

	perHour := big.NewInt(0).Mul(income, big.NewInt(3600))
	perHourF := big.NewFloat(0).SetInt(perHour)
	total := big.NewFloat(0).Quo(perHourF, big.NewFloat(params.Ether))
	p.Income, _ = total.Float64()
	return p, nil
}

func loadPeersData(ctx context.Context) (map[string]PeerPoint, error) {
	rvCtx, cancelRv := context.WithTimeout(ctx, 60*time.Second)
	info, err := rv.Info(rvCtx, &sonm.Empty{})
	if err != nil {
		return nil, err
	}
	cancelRv()
	log.Printf("peers count: %d\n", len(info.State))

	peers := map[string]PeerPoint{}
	for addr, state := range info.GetState() {
		for _, srv := range state.GetServers() {
			parts := strings.Split(addr, "//")
			peerEth := common.HexToAddress(parts[1])

			point, err := loadDeals(ctx, dwh, peerEth)
			if err != nil {
				log.Printf("failed to query DWH: %v\n", err)
				continue
			}

			ip := net.ParseIP(srv.PublicAddr.Addr.Addr)
			rec, err := db.City(ip)
			if err != nil {
				log.Printf("cannot find IP `%s` with geoip: %v\n", ip.String(), err)
				continue
			}

			point.Lat = rec.Location.Latitude
			point.Lon = rec.Location.Longitude
			peers[peerEth.Hex()] = point
		}
	}

	return peers, nil
}

func main() {
	log.Println("starting map proxy")
	go startPprof()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initConnections(ctx)
	defer db.Close()

	peers, err := loadPeersData(ctx)
	if err != nil {
		log.Printf("failed to load initial data from rv: %v\n", err)
		return
	}
	data := cache{green: peers}

	tk := time.NewTicker(30 * time.Second)
	defer tk.Stop()

	go func() {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			log.Println("handling http request")
			w.Header().Add("Content-Type", "application/json")
			w.Header().Add("Access-Control-Allow-Origin", "*")

			points := data.get()
			b, _ := json.Marshal(points)
			w.Write(b)
		})

		log.Printf("starting http server at %s\n", listedAddr)
		log.Fatal(http.ListenAndServe(listedAddr, nil))
		cancel()
	}()

	for {
		select {
		case <-ctx.Done():
			log.Println("context cancelled")
			os.Exit(0)
		case <-tk.C:
			peers, err := loadPeersData(ctx)
			if err != nil {
				log.Printf("failed to update peers list: %v\n", err)
				continue
			}

			data.update(peers)
		}
	}
}

func startPprof() {
	log.Println("starting pprof server")

	listener, err := net.Listen("tcp", "localhost:6060")
	if err != nil {
		log.Printf("failed to create pprof listener: %v\n", err)
		return
	}
	defer listener.Close()

	handler := http.NewServeMux()
	handler.HandleFunc(pprofPrefix+"/", pprof.Index)
	handler.HandleFunc(pprofPrefix+"/cmdline", pprof.Cmdline)
	handler.HandleFunc(pprofPrefix+"/profile", pprof.Profile)
	handler.HandleFunc(pprofPrefix+"/symbol", pprof.Symbol)
	handler.HandleFunc(pprofPrefix+"/trace", pprof.Trace)

	if err := http.Serve(listener, handler); err != nil {
		log.Printf("pprof server terminated: %v\n", err)
	}
}
