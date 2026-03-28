package dc

import "fmt"

// Telegram DC addresses (IPv4, port 443).
// Negative IDs are media DCs (mapped to their positive counterparts).
var dcAddrs = map[int16]string{
	1: "149.154.175.53:443",
	2: "149.154.167.51:443",
	3: "149.154.175.100:443",
	4: "149.154.167.91:443",
	5: "91.108.56.130:443",
}

// List holds DC address mappings.
type List struct {
	addrs map[int16]string
}

// New creates a DC list with default Telegram DC addresses.
func New() *List {
	addrs := make(map[int16]string, len(dcAddrs))
	for k, v := range dcAddrs {
		addrs[k] = v
	}
	return &List{addrs: addrs}
}

// Get returns the address for the given DC ID.
// Negative DC IDs (media DCs) are mapped to the corresponding positive DC.
func (l *List) Get(dcID int16) (string, error) {
	if dcID < 0 {
		dcID = -dcID
	}
	addr, ok := l.addrs[dcID]
	if !ok {
		return "", fmt.Errorf("unknown DC ID: %d", dcID)
	}
	return addr, nil
}
