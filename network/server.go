package network

import (
	"syscall"
	"time"

	"github.com/panjf2000/gnet/v2"
	"github.com/sirupsen/logrus"
)

type Server struct {
	gnet.BuiltinEventEngine
	engine gnet.Engine

	Network   string // tcp/udp/unix
	Address   string
	Options   []gnet.Option
	SoftLimit int
	HardLimit int
	// TODO: Clients should be moved to the proxy struct
	Clients map[string]*Client
}

func GetRLimit() syscall.Rlimit {
	var limits syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limits); err != nil {
		logrus.Error(err)
	}
	logrus.Infof("Current system soft limit: %d", limits.Cur)
	logrus.Infof("Current system hard limit: %d", limits.Max)
	return limits
}

func (s *Server) OnBoot(engine gnet.Engine) gnet.Action {
	s.engine = engine

	// Get the current limits
	limits := GetRLimit()

	// Set the soft and hard limits if they are not set
	if s.SoftLimit == 0 {
		s.SoftLimit = int(limits.Cur)
		logrus.Info("Soft limit is not set, using the current system soft limit")
	}

	if s.HardLimit == 0 {
		s.HardLimit = int(limits.Max)
		logrus.Info("Hard limit is not set, using the current system hard limit")
	}

	logrus.Infof("PostgreSQL server is listening on %s\n", s.Address)
	return gnet.None
}

func (s *Server) OnOpen(c gnet.Conn) (out []byte, action gnet.Action) {
	if s.engine.CountConnections() >= s.SoftLimit {
		logrus.Warn("Soft limit reached")
	}
	if s.engine.CountConnections() >= s.HardLimit {
		logrus.Error("Hard limit reached")
		c.Write([]byte("Hard limit reached\n"))
		c.Close()
		return nil, gnet.Close
	}

	if _, ok := s.Clients[c.RemoteAddr().String()]; !ok {
		s.Clients[c.RemoteAddr().String()] = NewClient("tcp", "localhost:5432", 4096)
	}

	logrus.Infof("PostgreSQL server is opening a connection from %s", c.RemoteAddr().String())
	return nil, gnet.None
}

func (s *Server) OnClose(c gnet.Conn, err error) (action gnet.Action) {
	logrus.Infof("PostgreSQL server is closing a connection from %s", c.RemoteAddr().String())
	s.Clients[c.RemoteAddr().String()].Close()
	return gnet.Close
}

func (s *Server) OnTraffic(c gnet.Conn) gnet.Action {
	// buf contains the data from the client (query)
	buf, _ := c.Next(-1)

	// TODO: parse the buffer and send the response or error
	// TODO: This is a very basic implementation of the gateway
	// and it is synchronous. I should make it asynchronous.
	logrus.Infof("Received %d bytes from %s", len(buf), c.RemoteAddr().String())
	s.Clients[c.RemoteAddr().String()].Send(buf)
	size, response := s.Clients[c.RemoteAddr().String()].Receive()
	// Write writes the response to the client
	c.Write(response[:size])

	// logrus.Infof("Received data: %s", string(buf))
	return gnet.None
}

func (s *Server) OnShutdown(engine gnet.Engine) {
	logrus.Println("PostgreSQL server is shutting down...")
}

func (s *Server) OnTick() (delay time.Duration, action gnet.Action) {
	logrus.Println("PostgreSQL server is ticking...")
	logrus.Infof("Active connections: %d", s.engine.CountConnections())
	return time.Second * 5, gnet.None
}

func (s *Server) Run() {
	err := gnet.Run(s, s.Network+"://"+s.Address, s.Options...)
	if err != nil {
		logrus.Error(err)
	}
}