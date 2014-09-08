package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	zmq "github.com/alecthomas/gozmq"
	"github.com/cascades-fbp/cascades-http/utils"
	"github.com/cascades-fbp/cascades/runtime"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var (
	urlEndpoint      = flag.String("port.url", "", "Component's input port endpoint")
	methodEndpoint   = flag.String("port.method", "", "Component's input port endpoint")
	headersEndpoint  = flag.String("port.headers", "", "Component's input port endpoint")
	formEndpoint     = flag.String("port.form", "", "Component's input port endpoint")
	responseEndpoint = flag.String("port.resp", "", "Component's output port endpoint")
	bodyEndpoint     = flag.String("port.body", "", "Component's output port endpoint")
	errorEndpoint    = flag.String("port.err", "", "Component's error port endpoint")
	jsonFlag         = flag.Bool("json", false, "Print component documentation in JSON")
	debug            = flag.Bool("debug", false, "Enable debug mode")
)

func assertError(err error) {
	if err != nil {
		fmt.Println("ERROR:", err.Error())
		os.Exit(1)
	}
}

func main() {
	flag.Parse()

	if *jsonFlag {
		doc, _ := registryEntry.JSON()
		fmt.Println(string(doc))
		os.Exit(0)
	}

	if *urlEndpoint == "" || *methodEndpoint == "" {
		flag.Usage()
		os.Exit(1)
	}
	if *responseEndpoint == "" && *bodyEndpoint == "" {
		flag.Usage()
		os.Exit(1)
	}

	log.SetFlags(0)
	if *debug {
		log.SetOutput(os.Stdout)
	} else {
		log.SetOutput(ioutil.Discard)
	}

	var err error

	context, _ := zmq.NewContext()
	defer context.Close()

	// Url socket
	urlSock, err := context.NewSocket(zmq.PULL)
	assertError(err)
	defer urlSock.Close()
	err = urlSock.Bind(*urlEndpoint)
	assertError(err)
	// Method socket
	methodSock, err := context.NewSocket(zmq.PULL)
	assertError(err)
	defer methodSock.Close()
	err = methodSock.Bind(*methodEndpoint)
	assertError(err)
	// Headers socket
	var headersSock *zmq.Socket
	if *headersEndpoint != "" {
		headersSock, err = context.NewSocket(zmq.PULL)
		assertError(err)
		defer headersSock.Close()
		err = headersSock.Bind(*headersEndpoint)
		assertError(err)
	}
	// Data socket
	var formSock *zmq.Socket
	if *formEndpoint != "" {
		formSock, err = context.NewSocket(zmq.PULL)
		assertError(err)
		defer formSock.Close()
		err = formSock.Bind(*formEndpoint)
		assertError(err)
	}

	// Response socket
	var respSock *zmq.Socket
	if *responseEndpoint != "" {
		respSock, err = context.NewSocket(zmq.PUSH)
		assertError(err)
		defer respSock.Close()
		err = respSock.Connect(*responseEndpoint)
		assertError(err)
	}
	// Error socket
	var errSock *zmq.Socket
	if *errorEndpoint != "" {
		errSock, err = context.NewSocket(zmq.PUSH)
		assertError(err)
		defer errSock.Close()
		err = errSock.Connect(*errorEndpoint)
		assertError(err)
	}
	// Response body socket
	var bodySock *zmq.Socket
	if *bodyEndpoint != "" {
		bodySock, err = context.NewSocket(zmq.PUSH)
		assertError(err)
		defer bodySock.Close()
		err = bodySock.Connect(*bodyEndpoint)
		assertError(err)
	}

	// Ctrl+C handling
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		for _ = range ch {
			log.Println("Give 0MQ time to deliver before stopping...")
			time.Sleep(1e9)
			log.Println("Stopped")
			os.Exit(0)
		}
	}()

	//TODO: setup input ports monitoring to close sockets when upstreams are disconnected

	// Setup socket poll items
	pollItems := zmq.PollItems{
		zmq.PollItem{Socket: urlSock, Events: zmq.POLLIN},
		zmq.PollItem{Socket: methodSock, Events: zmq.POLLIN},
	}
	if headersSock != nil {
		pollItems = append(pollItems, zmq.PollItem{Socket: headersSock, Events: zmq.POLLIN})
	}
	if formSock != nil {
		pollItems = append(pollItems, zmq.PollItem{Socket: formSock, Events: zmq.POLLIN})
	}

	// This is obviously dangerous but we need it to deal with our custom CA's
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}
	client.Timeout = 30 * time.Second

	// Main loop
	var (
		socket      *zmq.Socket = nil
		ip          [][]byte
		URL, method string
		headers     map[string][]string
		data        url.Values
		request     *http.Request
	)
	log.Println("Started")
	for {
		_, err = zmq.Poll(pollItems, -1)
		if err != nil {
			log.Println("Error polling ports:", err.Error())
			continue
		}
		socket = nil
		for _, item := range pollItems {
			if item.REvents&zmq.POLLIN != 0 {
				socket = item.Socket
				break
			}
		}
		if socket == nil {
			log.Println("ERROR: could not find socket in polling items array")
			continue
		}
		ip, err = socket.RecvMultipart(0)
		if err != nil {
			log.Println("Error receiving message:", err.Error())
			continue
		}
		if !runtime.IsValidIP(ip) {
			log.Println("Invalid IP:", ip)
			continue
		}
		switch socket {
		case urlSock:
			URL = string(ip[1])
			log.Println("URL specified:", URL)
		case methodSock:
			method = strings.ToUpper(string(ip[1]))
			log.Println("Method specified:", method)
		case headersSock:
			err = json.Unmarshal(ip[1], &headers)
			if err != nil {
				log.Println("ERROR: failed to unmarchal headers:", err.Error())
				continue
			}
			log.Println("Headers specified:", headers)
		case formSock:
			err = json.Unmarshal(ip[1], &data)
			if err != nil {
				log.Println("ERROR: failed to unmarchal form data:", err.Error())
				continue
			}
			log.Println("Form specified:", data)
		default:
			log.Println("ERROR: IP from unhandled socket received!")
			continue
		}

		if method == "" || URL == "" || (headersSock != nil && headers == nil) || (formSock != nil && data == nil) {
			continue
		}

		if data != nil {
			request, err = http.NewRequest(method, URL, strings.NewReader(data.Encode()))
		} else {
			request, err = http.NewRequest(method, URL, nil)
		}
		assertError(err)
		for k, v := range headers {
			request.Header.Add(k, v[0])
		}

		response, err := client.Do(request)
		if err != nil {
			log.Printf("ERROR performing HTTP %s %s: %s", request.Method, request.URL, err.Error())
			if errSock != nil {
				errSock.SendMultipart(runtime.NewPacket([]byte(err.Error())), zmq.NOBLOCK)
			}
			method = ""
			URL = ""
			headers = nil
			data = nil
			continue
		}
		resp, err := utils.Response2Response(response)
		if err != nil {
			log.Printf("ERROR converting response to reply: %s", err.Error())
			if errSock != nil {
				errSock.SendMultipart(runtime.NewPacket([]byte(err.Error())), zmq.NOBLOCK)
			}
			method = ""
			URL = ""
			headers = nil
			data = nil
			continue
		}
		ip, err = utils.Response2IP(resp)
		if err != nil {
			log.Printf("ERROR converting reply to IP: %s", err.Error())
			if errSock != nil {
				errSock.SendMultipart(runtime.NewPacket([]byte(err.Error())), zmq.NOBLOCK)
			}
			method = ""
			URL = ""
			headers = nil
			data = nil
			continue
		}

		if respSock != nil {
			respSock.SendMultipart(ip, 0)
		}
		if bodySock != nil {
			bodySock.SendMultipart(runtime.NewPacket(resp.Body), 0)
		}

		method = ""
		URL = ""
		headers = nil
		data = nil
	}
}
