package selftest

import "sync"

// Server is in a helper dir whose own test does not import it.
type Server struct{ wg sync.WaitGroup }

func (s *Server) Close() {
	s.wg.Wait() // want wait
}
