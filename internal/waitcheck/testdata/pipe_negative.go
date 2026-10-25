package p

func pipePair() (net.Conn, net.Conn) { return net.Pipe() }

func TestA(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, b := pipePair()
		_, _ = a, b
	})
}
