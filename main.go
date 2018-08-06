package main

import (
	"fmt"
	"golang.org/x/crypto/ssh"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"path"
	"bufio"
	"strings"
	"sync"
)

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

func main() {
	log.Println("Fortress Machine")

	hostKeyPath := path.Join(os.Getenv("HOME"), "config/sshfm")
	log.Println(hostKeyPath)
	hostKeyBytes, err := ioutil.ReadFile(hostKeyPath)
	if err != nil {
		log.Fatalln("Load private key sshfm failed")
	}

	hostKey, err := ssh.ParsePrivateKey(hostKeyBytes)
	if err != nil {
		log.Fatalln("Parse private key sshfm failed")
	}

	serverConfig := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			extensions := map[string]string{"username": "hago",
				"password":  "123",
				"sshserver": "10.21.16.202:22"}
			permissions := &ssh.Permissions{Extensions: extensions}

			return permissions, nil
		}}
	serverConfig.AddHostKey(hostKey)

	listener, err := net.Listen("tcp", ":6001")
	if err != nil {
		log.Fatalln("Listen on 6001 failed")
	}

	log.Println("Listening on 6001...")
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Accept incomming connnection failed, %s\n", err)
			continue
		}

		serverconn, channels, requests, err := ssh.NewServerConn(conn, serverConfig)
		if err != nil {
			log.Printf("Handshake failed, %s\n", err)
			continue
		}

		connection := &Connection{serverConn: serverconn,
					incommingChannels: channels,
					incommingRequests: requests}
		go connection.handle()
	}
}

type Connection struct {
	serverConn  *ssh.ServerConn
	incommingChannels <-chan ssh.NewChannel
	incommingRequests <-chan *ssh.Request

	client  *ssh.Client

	mutex sync.Mutex
	stopAudit bool
}

func (c *Connection) handle() {
	defer c.serverConn.Wait()

	log.Printf("[%s] from [%s]\n", c.serverConn.User(), c.serverConn.RemoteAddr())

	// 忽略全局请求
	go ssh.DiscardRequests(c.incommingRequests)

	// 连接远程服务端
	clientConfig := &ssh.ClientConfig{
		User: c.serverConn.Permissions.Extensions["username"],
		Auth: []ssh.AuthMethod{
			ssh.Password(c.serverConn.Permissions.Extensions["password"]),
		},

		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	client, err := ssh.Dial("tcp", c.serverConn.Permissions.Extensions["sshserver"], clientConfig)
	if err != nil {
		log.Println(err)
		return
	}
	defer client.Close()

	c.client = client

	// 处理通道请求, 我们只处理session请求
	for newchan := range c.incommingChannels {
		if chanType := newchan.ChannelType(); chanType != "session" {
			newchan.Reject(ssh.UnknownChannelType, fmt.Sprintf("unknown channel type: %s", chanType))
			continue
		}

		channel, requests, err := newchan.Accept()
		if err != nil {
			log.Printf("Accept channel failed, %s\n", err)
			continue
		}

		session, err := client.NewSession()
		if err != nil {
			channel.Close()
			log.Printf("Failed to create session: %s\n", err)
			continue
		}

		c.handleChannel(channel, requests, session)
	}
}

func (c *Connection) handleChannel(channel ssh.Channel, requests <-chan *ssh.Request, session *ssh.Session) {
	defer channel.Close()
	defer session.Close()

	// 转发请求
	go func() {
		for request := range requests {
			success, err := session.SendRequest(request.Type, request.WantReply, request.Payload)
			if err != nil {
				log.Printf("Send request failed, %s", err)
			}

			if (request.Type == "exec") {
				command := string(request.Payload[4:])
				if strings.HasPrefix(command, "scp") {
					c.mutex.Lock()
					c.stopAudit = true
					c.mutex.Unlock()

					log.Printf("[%s@%s] %s\n", c.serverConn.User(), c.client.RemoteAddr(), command)
				} else {
					c.mutex.Lock()
					c.stopAudit = false
					c.mutex.Unlock()
				}
			}

			if request.WantReply {
				request.Reply(success, nil)
			}
		}
	}()


	// 转发数据，同时监听数据
	sessionStdin, err := session.StdinPipe()
	if err != nil {
		log.Println(err)
	}

	sessionStdout, err := session.StdoutPipe()
	if err != nil {
		log.Println(err)
	}

	sessionStderr, err := session.StderrPipe()
	if err != nil {
		log.Println(err)
	}

	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()

	go io.Copy(sessionStdin, channel)
	go io.Copy(channel, sessionStderr)

	go c.audit(r)
	mw := io.MultiWriter(channel, w)
	io.Copy(mw, sessionStdout)
}

func (c *Connection) audit(r io.Reader) {
	b := bufio.NewReader(r)

	for {
		line, _, err := b.ReadLine()
		if err != nil {
			if err != io.EOF && err != io.ErrClosedPipe {
				log.Println(err)
			}

			break
		}

		c.mutex.Lock()
		if !c.stopAudit {
			log.Printf("[%s@%s] %s\n", c.serverConn.User(), c.client.RemoteAddr(), line)
		}
		c.mutex.Unlock()
	}
}

