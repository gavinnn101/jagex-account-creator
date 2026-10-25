package p

func readLine(r *bufio.Reader) { _, _ = r.ReadString('\n') }

func dialRaw() (net.Conn, error) {
	c, err := net.Dial("tcp", "x")
	return c, err
}

func TestLate(t *testing.T) {
	c, _ := net.Dial("tcp", "x") // want conn
	_, _ = bufio.NewReader(c).ReadString('\n')
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
}

func TestHelper(t *testing.T) {
	c, _ := net.Dial("tcp", "x") // want conn
	readLine(bufio.NewReader(c))
}

func TestDialer(t *testing.T) {
	c, _ := dialRaw() // want conn
	_, _ = c.Read(nil)
}
