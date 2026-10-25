package p

func TestA(t *testing.T) {
	var c net.Conn
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
}
