package main

import (
	"edc"
	"flag"
	"fmt"
	"github.com/fitstar/falcore"
	"github.com/fitstar/falcore/filter"
)

func main() {
	port := flag.Int("port", 8080, "server port")
	domain := flag.String("domain", "", "cookie domain")
	assetBase := flag.String("assets", "assets", "path to file assets")
	flag.Parse()
	// setup pipeline
	pipeline := falcore.NewPipeline()

	ww := edc.NewWebsocketWorker(*domain, *assetBase)
	edc.NewController("localhost", 6379, ww)

	// file stuff
	ff := &filter.FileFilter{
		BasePath:   *assetBase,
		PathPrefix: "/assets",
	}

	// upstream
	pipeline.Upstream.PushBack(ww)
	pipeline.Upstream.PushBack(ff)

	// setup server
	server := falcore.NewServer(*port, pipeline)

	server.WebsocketHandler = ww.WebsocketHandler
	server.WebsocketUpgrade = ww.WebsocketUpgrade
	// start the server
	// this is normally blocking forever unless you send lifecycle commands
	falcore.Info("Server started on %v", *port)
	if err := server.ListenAndServe(); err != nil {
		fmt.Println("Could not start server:", err)
	}
}
