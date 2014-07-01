package sshproxy

import (
	"bytes"
	"code.google.com/p/go.crypto/ssh"
	"fmt"
	"net"
)

func CheckHostKey(HostKey string) (checkHostKey func(string, net.Addr, ssh.PublicKey) error) {
	var err error
	var public ssh.PublicKey
	var publices []ssh.PublicKey
	rest := []byte(HostKey)
	for {
		public, _, _, rest, err = ssh.ParseAuthorizedKey(rest)
		if err != nil {
			err = nil
			break
		}
		publices = append(publices, public)
	}

	checkHostKey = func(hostname string, remote net.Addr, key ssh.PublicKey) (err error) {
		log.Debug("check hostkey: %s", hostname)
		hostkey := key.Marshal()
		log.Info("remote hostkey: %s", key.Type())

		for _, public := range publices {
			if key.Type() == public.Type() && bytes.Compare(hostkey, public.Marshal()) == 0 {
				log.Info("host key match: %s", hostname)
				return nil
			}
		}
		log.Info("host key not match: %s", hostname)
		return ErrHostKey
	}
	return
}

func genClientConfig(Username, PrivateKey, HostKey string) (config *ssh.ClientConfig, err error) {
	private, err := ssh.ParsePrivateKey([]byte(PrivateKey))
	if err != nil {
		log.Error("failed to parse keyfile: %s", err.Error())
		return
	}

	config = &ssh.ClientConfig{
		User:            Username,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(private)},
		HostKeyCallback: CheckHostKey(HostKey),
	}
	return
}

func (ci *ConnInfo) clientBuilder() (client ssh.Conn, chans <-chan ssh.NewChannel, reqs <-chan *ssh.Request, err error) {
	// load private key from user and host
	var PrivateKey string
	err = ci.srv.db.QueryRow("SELECT keys FROM accounts WHERE username=? AND host=?",
		ci.Username, ci.Host).Scan(&PrivateKey)
	if err != nil {
		log.Error("%s", err.Error())
		return
	}

	// and try connect it as last step
	hostname := fmt.Sprintf("%s:%d", ci.Hostname, ci.Port)
	var conn net.Conn
	switch {
	case ci.ProxyCommand == "":
		conn, err = net.Dial("tcp", hostname)
		if err != nil {
			log.Error("ssh dial failed: %s", err.Error())
			return
		}
	case ci.ProxyAccount != 0:
	default:
		// FIXME: dangerous
		log.Info("proxy command: %s", ci.ProxyCommand)
		conn, err = RunCmdNet(ci.ProxyCommand)
		if err != nil {
			log.Error("proxy command failed: %s", err.Error())
			return
		}
	}

	config, err := genClientConfig(ci.Username, PrivateKey, ci.Hostkeys)
	if err != nil {
		return
	}
	client, chans, reqs, err = ssh.NewClientConn(conn, hostname, config)
	if err != nil {
		log.Error("ssh client conn failed: %s", err.Error())
		return
	}
	return
}

func (ci *ConnInfo) ConnAccount(accountid int, desthost string, destport int) (conn net.Conn, err error) {
	var port int
	var username, keys, hostname, hostkeys string
	err = ci.srv.db.QueryRow(
		"SELECT username, keys, hostname, port, hostkeys FROM accounts WHERE id=?",
		accountid).Scan(&username, &keys, &hostname, &port, &hostkeys)
	if err != nil {
		log.Error("%s", err.Error())
		return
	}

	config, err := genClientConfig(username, keys, hostkeys)
	if err != nil {
		return
	}

	hostname = fmt.Sprintf("%s:%d", hostname, port)
	client, err := ssh.Dial("tcp", hostname, config)
	if err != nil {
		return
	}

	session, err := client.NewSession()
	if err != nil {
		return
	}

	err = session.Run(fmt.Sprintf("nc %s %p", desthost, destport))
	if err != nil {
		return
	}

	cn := &CmdNet{
		w:    session,
		c:    client,
		name: "ssh",
	}
	cn.stdin, err = session.StdinPipe()
	if err != nil {
		return
	}
	cn.stdout, err = session.StdoutPipe()
	if err != nil {
		return
	}

	return cn, nil
}
