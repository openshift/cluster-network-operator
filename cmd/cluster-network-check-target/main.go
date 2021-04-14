package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
)

const port = "8080"

var nodeName = os.Getenv("K8S_NODE_NAME")

func checktargetHandler(w http.ResponseWriter, r *http.Request) {
	// This is mostly to make the check-target slightly useful for user debugging
	// purposes. If we want the returned data to be useful to the check-source then we
	// should return it as HTTP headers too. (But since the checker doesn't currently
	// use any of that data, we don't bother.)

	client, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		client = r.RemoteAddr
	}

	server := "unknown IP"
	addr := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if tcpAddr, ok := addr.(*net.TCPAddr); ok {
		server = tcpAddr.IP.String()
	}

	fmt.Fprintf(w, "Hello, %s. You have reached %s on %s", client, server, nodeName)
}

func listenAndServe(port string) {
	fmt.Printf("serving on %s\n", port)
	err := http.ListenAndServe(":"+port, nil)
	if err != nil {
		panic("ListenAndServe: " + err.Error())
	}
}

func main() {
	http.HandleFunc("/", checktargetHandler)
	go listenAndServe(port)

	select {}
}
