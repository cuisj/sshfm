package main

import (
	"log"
	"os"
	"path"
	"io"
	"io/ioutil"
	"net"
	"fmt"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/terminal"
)

func main() {
	log.Println("Fortress Machine")

	hostKeyPath := path.Join(os.Getenv("GOPATH"), "bin/config/sshfm")

	hostKeyBytes, err := ioutil.ReadFile(hostKeyPath)
	if err != nil {
		log.Fatalln("Load private key sshfm failed")
	}

	hostKey, err := ssh.ParsePrivateKey(hostKeyBytes)
	if err != nil {
		log.Fatalln("Parse private key sshfm failed")
	}

	serverConfig := &ssh.ServerConfig {
				PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
						permissions := &ssh.Permissions{CriticalOptions: map[string]string{"force-command": "list"}}
						return permissions, nil
					}}
	serverConfig.AddHostKey(hostKey)

	listener, err := net.Listen("tcp", ":6080")
	if err != nil {
		log.Fatalln("Listen on 6080 failed")
	}

	log.Println("Listening on 6080...")
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Accept incomming connnection failed, %s\n", err)
			continue
		}

		serverConn, newChannel, globalRequest, err := ssh.NewServerConn(conn, serverConfig)
		if err != nil {
			log.Printf("Handshake failed, %s\n", err)
			continue
		}

		go handleConnection(serverConn, newChannel, globalRequest)
	}
}

func handleConnection(serverConn *ssh.ServerConn, newChannel <-chan ssh.NewChannel, globalRequest <-chan *ssh.Request) {
	defer serverConn.Wait()

	log.Printf("New SSH connection from %s(%s)\n", serverConn.RemoteAddr(), serverConn.User())

	go ssh.DiscardRequests(globalRequest)

	for newChannel := range newChannel {
		if chanType := newChannel.ChannelType(); chanType != "session" {
			newChannel.Reject(ssh.UnknownChannelType, fmt.Sprintf("unknown channel type: %s", chanType))
			continue
		}

		channel, request, err := newChannel.Accept()
		if err != nil {
			log.Printf("Accept channel failed, %s\n", err)
			continue
		}

		handleChannel(serverConn, channel, request)
	}
}

func handleChannel(serverConn *ssh.ServerConn, channel ssh.Channel, requests <-chan *ssh.Request) {
	originalRequests := make(chan *ssh.Request, 16)

	go func() {
		for request := range requests {
			originalRequests <- request

			if request.WantReply {
				request.Reply(true, nil)
			}
		}

		close(originalRequests)
	}()

	go func() {
		for request := range originalRequests {
			log.Printf("%s\n", request.Type)
		}
	}()


	term := terminal.NewTerminal(channel, "fm> ")
	defer channel.Close()

	fmt.Fprintln(term, "Welcome to Fortress Machine, Your resource list below:")
	fmt.Fprintln(term, "[1] 10.21.16.202")

	for {
		line, err := term.ReadLine()
		if err != nil {
			if err != io.EOF {
				log.Println(err)
			}
			break
		}

		if line != "list" {
			fmt.Fprintf(term, "You have no permission to execute [%s]\n", line)
			continue
		}

		fmt.Fprintln(term, "Just to controll you!")
	}
}
