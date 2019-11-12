package main

import (
	"bytes"
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
	"gopkg.in/yaml.v2"
)

type LoginJson struct {
	Username string `json:"user"`
	Password string `json:"password"`
}

type SockIoSIDResponse struct {
	Sid          string
	Upgrades     []string
	PingInterval int
	PingTimeout  int
}

type Config struct {
	GotifyKey string `yaml:"gotifykey"`
	Username  string `yaml:"username"`
	Password  string `yaml:"password"`
}

func (c *Config) fromFile(configFile string) error {
	b, err := ioutil.ReadFile(configFile)
	if err != nil {
		return err
	}

	return yaml.Unmarshal(b, c)
}

func makeLoginClosure(c *Config) func(*websocket.Conn) error {
	return func(socket *websocket.Conn) error {
		loginJson := &LoginJson{
			Username: c.Username,
			Password: c.Password,
		}

		loginJsonString, err := json.Marshal(loginJson)

		if err != nil {
			log.Fatal(err)
		}

		loginMessage := fmt.Sprintf("%s[\"auth\",%s]", LOGIN_DATA_PREFIX, loginJsonString)

		socket.WriteMessage(websocket.TextMessage, []byte(loginMessage))

		return nil
	}

}

var sockIOGarbageRegExp = regexp.MustCompile("^[^\\[\\{]*|[^\\]\\}]*$")

func removeSockIOGarbage(str string) string {
	return sockIOGarbageRegExp.ReplaceAllString(str, "")
}

func getSID(webSocketAddr *string) (string, error) {
	res, err := http.Get(fmt.Sprintf("https://%s/socket.io/?EIO=3&transport=polling", *webSocketAddr))
	if err != nil {
		return "", err
	}
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", err
	}

	JSONString := removeSockIOGarbage(string(body))

	var data SockIoSIDResponse

	json.Unmarshal([]byte(JSONString), &data)

	return data.Sid, nil
}

func connect(webSocketAddr *string) (*websocket.Conn, *http.Response, error) {
	sid, err := getSID(webSocketAddr)

	if err != nil {
		fmt.Println(err)
		log.Fatal(err)
	}

	uri := url.URL{
		Scheme:   "wss",
		Host:     *webSocketAddr,
		Path:     "/socket.io/",
		RawQuery: fmt.Sprintf("EIO=3&transport=websocket&sid=%s", url.QueryEscape(sid)),
	}

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

	return socket, resp, err
}

const LOGIN_DATA_PREFIX = "42"

type MessageArray struct {
	MessageType string
	Message     struct {
		Msg struct {
			From struct {
				Mode string `json:"mode"`
				Nick string `json:"nick"`
			} `json:"from"`
			Time      string `json:"time"`
			Type      string `json:"type"`
			Text      string `json:"text"`
			Highlight bool   `json:"highlight"`
		} `json:"msg"`
		Highlight int `json:"highlight"`
		Unread    int `json:"unread"`
	} `json:"msg"`
}

type Gotification struct {
	Title   string `json:"title"`
	Message string `json:"message"`
}

func makeGotifiacationClosure(url *string, apiKey *string) func(*Gotification) {
	return func(notifcation *Gotification) {
		fmt.Println("sending notification to gotify server")
		jsonString, err := json.Marshal(notifcation)

		if err != nil {
			log.Fatal(err)
		}

		req, err := http.NewRequest("POST", fmt.Sprintf("%s/message", *url), bytes.NewBuffer(jsonString))

		if err != nil {
			fmt.Println(err)
		}

		req.Header.Set("X-Gotify-Key", *apiKey)
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{}

		resp, err := client.Do(req)

		if err != nil {
			fmt.Println(err)
		}

		body, _ := ioutil.ReadAll(resp.Body)

		fmt.Println(resp.Status, string(body))

		defer resp.Body.Close()
	}
}

func makeHandleMessageClosure(socket *websocket.Conn, gotifyer func(*Gotification), login func(*websocket.Conn) error) func([]byte) {
	return func(message []byte) {
		messageString := string(message)
		if messageString == "3probe" {
			fmt.Println("Received 3probe, nice! Logging in...")
			socket.WriteMessage(websocket.TextMessage, []byte("5"))
			login(socket)
			return
		}

		if messageString == "3" {
			fmt.Println("Received 3, so sending 2 was usefull (or something like that)")
			return
		}

		messageArray := &MessageArray{}

		messageSlice := []interface{}{&messageArray.MessageType, &messageArray.Message}

		messageString = removeSockIOGarbage(messageString)

		err := json.Unmarshal([]byte(messageString), &messageSlice)

		if err != nil {
			fmt.Println(err)
		}

		fmt.Printf("Received message of type: %s\n", messageArray.MessageType)

		messageStruct := messageArray.Message

		if messageArray.MessageType == "msg" &&
			messageStruct.Msg.Highlight {
			fmt.Printf("Received message from %s containing %s\n", messageStruct.Msg.From.Nick, messageStruct.Msg.Text)

			gotification := &Gotification{
				Title:   fmt.Sprintf("Message from %s", messageStruct.Msg.From.Nick),
				Message: messageStruct.Msg.Text,
			}

			gotifyer(gotification)
		}
	}
}

func startMainLoop(socket *websocket.Conn, gotifyer func(*Gotification), c *Config) {
	defer func() {
		err := socket.Close()

		if err != nil {
			fmt.Println(err)
		}
	}()

	done := make(chan struct{})

	login := makeLoginClosure(c)
	handleMessage := makeHandleMessageClosure(socket, gotifyer, login)

	go func() {
		defer close(done)
		for {
			_, message, err := socket.ReadMessage()
			if err != nil {
				log.Println("read:", err)
				return
			}

			handleMessage(message)
		}
	}()

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	// Socket.io wants some probing to happen
	socket.WriteMessage(websocket.TextMessage, []byte("2probe"))

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

		case <-done:
			return

		case <-time.After(25 * time.Second):
			fmt.Println("Sending 2")
			socket.WriteMessage(websocket.TextMessage, []byte("2"))
		}
	}
}

func main() {
	webSocketAddr := flag.String("addr", "irc.hugot.nl:443", "TheLounge host")
	gotifyUrl := flag.String("gotify-url", "https://gotify.code-cloppers.com", "Gotify HTTP address")
	//gotifyKey := flag.String("gotify-key", "", "Gotify application key")

	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Println("Missing required parameter: CONFIG_PATH")
		flag.Usage()
		os.Exit(1)
	}

	envPath := flag.Arg(0)

	var c Config
	err := c.fromFile(envPath)
	if err != nil {
		log.Fatal(err)
	}

	gotifyKey := &c.GotifyKey

	log.SetFlags(0)

	socket, resp, err := connect(webSocketAddr)

	if err != nil {
		if err == websocket.ErrBadHandshake {
			log.Printf("handshake failed with status %d", resp.StatusCode)
		}

		log.Fatal("dial:", err)
	}

	gotifyer := makeGotifiacationClosure(gotifyUrl, gotifyKey)
	startMainLoop(socket, gotifyer, &c)
}
