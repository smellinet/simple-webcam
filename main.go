package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"strconv"

	"simple-webcam/broker"
	h "simple-webcam/helper"
	"simple-webcam/raspivid"

	"github.com/gorilla/websocket"
)

var clients = make(map[*websocket.Conn]bool)
var clientsMotion = make(map[*websocket.Conn]bool)
var camera raspivid.Camera
var motion raspivid.Motion

var recorder raspivid.Recorder

func streamVideoToWS(ws *websocket.Conn, caster *broker.Broker, quit chan bool) {
	stream := caster.Subscribe()
	var x interface{}
	//f, _ := os.Create("temp.h264")
	for {
		select {
		case <-quit:
			log.Println("Ending a WS video stream")
			return
		default:
			x = <-stream
			ws.WriteMessage(websocket.BinaryMessage, x.([]byte))
			//f.Write(x.([]byte))
			//		log.Println("sending--------------\n" + hex.Dump(x.([]byte)))
			//		log.Println("sent-----------------")
			//log.Println("sent bytes: " + strconv.Itoa(len(x.([]byte))))
		}
	}
}

func streamMotionToWS(ws *websocket.Conn, caster *broker.Broker, quit chan bool) {
	stream := caster.Subscribe()
	var x interface{}
	//f, _ := os.Create("motion.vec")
	for {
		select {
		case <-quit:
			log.Println("Ending a WS motion stream")
			return
		default:
			x = <-stream
			ws.WriteMessage(websocket.BinaryMessage, x.([]byte))
			//f.Write(x.([]byte))
			//		log.Println("sending--------------\n" + hex.Dump(x.([]byte)))
			//		log.Println("sent-----------------")
			//log.Println("sent bytes: " + strconv.Itoa(len(x.([]byte))))
		}
	}
}

func initClientVideo(ws *websocket.Conn) {
	type initVideo struct {
		Action  string `json:"action"`
		Width   int    `json:"width"`
		Height  int    `json:"height"`
		MBwidth int    `json:"mbWidth"`
	}

	settings := initVideo{
		"init",
		*camera.Width,
		*camera.Height,
		motion.BlockWidth,
	}

	message, err := json.Marshal(settings)
	h.CheckError(err)
	//log.Println("Initializing client with: " + string(message))

	ws.WriteMessage(websocket.TextMessage, message)
}

func wsHandler(caster *broker.Broker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		}
		upgrader.CheckOrigin = func(r *http.Request) bool { return true }

		// upgrade this connection to a WebSocket connection
		ws, err := upgrader.Upgrade(w, r, nil)
		h.CheckError(err)
		defer ws.Close()

		clients[ws] = true
		log.Println("Client Connected")

		initClientVideo(ws)

		quit := make(chan bool)
		requestStreamStatus := false
		for {
			// read in a message
			messageType, p, err := ws.ReadMessage()
			//		log.Println("Message Type: " + strconv.Itoa(messageType))
			if err != nil {
				delete(clients, ws)
				//log.Println(err)
				quit <- true
				break
			}

			if messageType == websocket.TextMessage {
				log.Println("Message Received: " + string(p))
				switch string(p) {
				case "start":
					if !requestStreamStatus {
						requestStreamStatus = true
						go streamVideoToWS(ws, caster, quit)
					} else {
						log.Println("Already requested stream")
					}
				case "stop":
					quit <- true
					requestStreamStatus = false
				case "mode:night":
					camera.CameraNightMode <- true
				case "mode:day":
					camera.CameraNightMode <- false
				case "startrecord":
					recorder.RequestedRecord = true
				case "stoprecord":
					recorder.RequestedRecord = false
				}
			}
		}
	})
}

func wsHandlerMotion(caster *broker.Broker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		}
		upgrader.CheckOrigin = func(r *http.Request) bool { return true }

		// upgrade this connection to a WebSocket connection
		ws, err := upgrader.Upgrade(w, r, nil)
		h.CheckError(err)
		defer ws.Close()

		clientsMotion[ws] = true
		log.Println("Client Connected for motion")

		quit := make(chan bool)
		requestStreamStatus := false
		for {
			// read in a message
			messageType, p, err := ws.ReadMessage()
			//		log.Println("Message Type: " + strconv.Itoa(messageType))
			if err != nil {
				delete(clientsMotion, ws)
				//log.Println(err)
				quit <- true
				break
			}

			if messageType == websocket.TextMessage {
				log.Println("Motion Message Received: " + string(p))
				switch string(p) {
				case "start":
					if !requestStreamStatus {
						requestStreamStatus = true
						go streamMotionToWS(ws, caster, quit)
					} else {
						log.Println("Already requested motion stream")
					}
				case "stop":
					quit <- true
					requestStreamStatus = false
				}
			}
		}
	})
}

func httpStreamHandler(caster *broker.Broker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Println("Starting HTTP stream")
		w.Header().Add("Content-Type", "video/H264")
		w.Header().Add("Transfer-Encoding", "chunked")
		w.WriteHeader(200)

		quit := w.(http.CloseNotifier).CloseNotify()

		seenHeader := false
		stream := caster.Subscribe()
		var x interface{}
	loop:
		for {
			select {
			case <-quit:
				break loop
			default:
				x = <-stream
				if seenHeader == false && x.([]byte)[4] == 39 { // SPS header
					seenHeader = true
				}

				if seenHeader {
					w.Write(x.([]byte))
				}
			}
		}

		log.Println("Ending HTTP stream")
	})
}

func main() {
	port := flag.Int("port", 8080, "Port to listen on")
	camera.Width = flag.Int("width", 1280, "Video width")
	camera.Height = flag.Int("height", 960, "Video height")
	camera.Fps = flag.Int("fps", 12, "Video framerate. Minimum 1 fps")
	camera.SensorMode = flag.Int("sensor", 0, "Sensor mode")
	camera.Bitrate = flag.Int("bitrate", 2000000, "Video bitrate")
	camera.Rotation = flag.Int("rot", 0, "Rotate 0, 90, 180, or 270 degrees")
	camera.Protocol = "tcp"
	camera.ListenPort = ":9000"
	camera.ListenPortMotion = ":9001"

	mNumAvgFrames := flag.Int("mframes", 4, "Number of motion frames to examine")
	mThreshold := flag.Int("mthreshold", 4, "Motion sensitivity. Lower # is more sensitive.")
	mBlockWidth := flag.Int("mblockwidth", 0, "Width of motion detection block. Video width and height be divisible by mblockwidth * 16")
	flag.Parse()
	motion.NumAvgFrames = *mNumAvgFrames
	motion.SenseThreshold = int8(*mThreshold)
	motion.BlockWidth = *mBlockWidth

	listenPort := ":" + strconv.Itoa(*port)
	if *camera.Bitrate < 1 || *camera.Fps < 1 {
		log.Fatal("FPS and bitrate must be greater than 1")
	}

	// setup motion detector
	motion.Protocol = "tcp"
	motion.ListenPort = ":9001"
	motion.Width = *camera.Width
	motion.Height = *camera.Height
	motion.Init()

	exDir, _ := os.Executable()
	exDir = filepath.Dir(exDir)

	// start broadcaster and camera
	castVideo := broker.New()
	castMotion := broker.New()
	go castVideo.Start()
	go castMotion.Start()

	go motion.Start(castMotion, &recorder)
	go camera.Start(castVideo, &recorder)
	go recorder.Init(castVideo, exDir+"/www/temp.h264")

	// setup web services
	fs := http.FileServer(http.Dir(exDir + "/www"))
	http.Handle("/", fs)
	http.Handle("/ws/video", wsHandler(castVideo))
	http.Handle("/ws/motion", wsHandlerMotion(castMotion))
	http.Handle("/video.h264", httpStreamHandler(castVideo))

	log.Println("HTTP Listening on " + listenPort)
	http.ListenAndServe(listenPort, nil)
}
