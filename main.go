package main

import (
	"fmt"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/terminal"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"path"
)

func init() {
	//log.SetFlags(log.LstdFlags | log.Lshortfile)
}

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

		serverConn, newChanChan, requestChan, err := ssh.NewServerConn(conn, serverConfig)
		if err != nil {
			log.Printf("Handshake failed, %s\n", err)
			continue
		}

		p := &Proxy{serverConn: serverConn, newChanChan: newChanChan, requestChan: requestChan}
		go p.handle()
	}
}

type Proxy struct {
	serverConn  *ssh.ServerConn
	newChanChan <-chan ssh.NewChannel
	requestChan <-chan *ssh.Request

	channel ssh.Channel
	request <-chan *ssh.Request

	client  *ssh.Client
	session *ssh.Session
}

func (p *Proxy) handle() {
	defer p.serverConn.Wait()

	log.Printf("[%s] from [%s]\n", p.serverConn.User(), p.serverConn.RemoteAddr())

	// 忽略全局请求
	go ssh.DiscardRequests(p.requestChan)

	// 连接远程服务端
	clientConfig := &ssh.ClientConfig{
		User: p.serverConn.Permissions.Extensions["username"],
		Auth: []ssh.AuthMethod{
			ssh.Password(p.serverConn.Permissions.Extensions["password"]),
		},

		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	client, err := ssh.Dial("tcp", p.serverConn.Permissions.Extensions["sshserver"], clientConfig)
	if err != nil {
		return
	}
	defer client.Close()

	// 处理通道请求, 我们只处理session请求
	for newChan := range p.newChanChan {
		if chanType := newChan.ChannelType(); chanType != "session" {
			newChan.Reject(ssh.UnknownChannelType, fmt.Sprintf("unknown channel type: %s", chanType))
			continue
		}

		ch, req, err := newChan.Accept()
		if err != nil {
			log.Printf("Accept channel failed, %s\n", err)
			continue
		}

		session, err := client.NewSession()
		if err != nil {
			log.Printf("Failed to create session: ", err)
			return
		}

		p.channel = ch
		p.request = req

		p.client = client
		p.session = session

		break
	}

	p.handleChannel()
}

func (p *Proxy) handleChannel() {
	defer p.channel.Close()
	defer p.session.Close()

	go func() {
		for request := range p.request {
			result, err := p.session.SendRequest(request.Type, request.WantReply, request.Payload)
			if err != nil {
				log.Printf("Send request failed, %v", err)
			}

			if request.WantReply {
				request.Reply(result, nil)
			}
		}
	}()

	stdout, _ := p.session.StdoutPipe()
	stderr, _ := p.session.StderrPipe()
	stdin, _ := p.session.StdinPipe()

	r, w := io.Pipe()
	defer w.Close()
	defer r.Close()

	mw := io.MultiWriter(stdin, w)

	go io.Copy(mw, p.channel)
	go io.Copy(p.channel, stdout)
	go p.audit(r)

	io.Copy(p.channel, stderr)
}

func (p *Proxy) audit(r io.Reader) {
	fc := NewFakeChannel(r)

	term := terminal.NewTerminal(fc, "fm> ")
	for {
		line, err := term.ReadLine()
		if err != nil {
			if err != io.EOF && err != io.ErrClosedPipe {
				log.Println(err)
			}
			break
		}

		if len(line) > 0 {
			log.Printf("[%s@%s] %s\n", p.serverConn.User(), p.client.RemoteAddr(), line)
		}
	}
}

type FakeChannel struct {
	r io.Reader
}

func NewFakeChannel(r io.Reader) *FakeChannel {
	return &FakeChannel{r: r}
}

func (t *FakeChannel) Read(p []byte) (n int, err error) {
	return t.r.Read(p)
}

func (t *FakeChannel) Write(p []byte) (n int, err error) {
	return len(p), nil
}
