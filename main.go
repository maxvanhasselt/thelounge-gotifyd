package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"time"

	"github.com/gorilla/websocket"
)

type SockIoSIDResponse struct {
	Sid          string
	Upgrades     []string
	PingInterval int
	PingTimeout  int
}

var websocket_addr = flag.String("addr", "irc.hugot.nl:443", "http service address")

var sockio_garbage_regexp = regexp.MustCompile("^[^\\[\\{]*|[^\\]\\}]*$")

func removeSockIOGarbage(str string) string {
	return sockio_garbage_regexp.ReplaceAllString(str, "")
}

func getSID() (string, error) {
	res, err := http.Get(fmt.Sprintf("https://%s/socket.io/?EIO=3&transport=polling", *websocket_addr))
	if err != nil {
		return "", err
	}
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", err
	}

	json_string := removeSockIOGarbage(string(body))

	var data SockIoSIDResponse

	json.Unmarshal([]byte(json_string), &data)

	return data.Sid, nil
}

func main() {
	sid, err := getSID()

	if err != nil {
		fmt.Println(err)
	}

	flag.Parse()
	log.SetFlags(0)

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	uri := url.URL{Scheme: "wss", Host: *websocket_addr, Path: fmt.Sprintf("/socket.io/?EIO=3&transport=websocket&sid=%s", sid)}

	dialer := &websocket.Dialer{
		Proxy:             http.ProxyFromEnvironment,
		HandshakeTimeout:  45 * time.Second,
		EnableCompression: true,
	}

	socket, resp, err := dialer.Dial(
		uri.String(),
		http.Header{
			"Cookie":        []string{fmt.Sprintf("io=%s", sid)},
			"Accept":        []string{"*/*"},
			"Pragma":        []string{"no-cache"},
			"Cache-Control": []string{"no-cache"},
		},
	)

	if err != nil {
		if err == websocket.ErrBadHandshake {
			log.Printf("handshake failed with status %d", resp.StatusCode)
		}

		log.Fatal("dial:", err)
	}
	defer socket.Close()

	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			_, message, err := socket.ReadMessage()
			if err != nil {
				log.Println("read:", err)
				return
			}
			log.Printf("recv: %s", message)
		}
	}()

	for {
		select {
		case <-done:
			return
		case <-interrupt:
			log.Println("interrupt")

			// Cleanly close the connection by sending a close message and then
			// waiting (with timeout) for the server to close the connection.
			err := socket.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			if err != nil {
				log.Println("write close:", err)
				return
			}
			select {
			case <-done:
			case <-time.After(time.Second):
			}
			return
		}
	}
}
