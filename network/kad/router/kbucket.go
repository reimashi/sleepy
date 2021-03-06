package router

import (
	"crypto/rand"
	"errors"
	"net"
	kadTypes "sleepy/network/kad/types"
	"sleepy/types"
	"sort"
	"sync"
)

const (
	maxBucketSize = 16 // Max number of peers in each k-bucket
	maxPeersPerIP = 3  // Number of peers permitted from same public IP
)

// K-bucket is a queue of k peers ordered by TTL
type kBucket struct {
	peers       []*kadTypes.Peer
	peersAccess sync.Mutex
}

func newKBucket() *kBucket {
	return &kBucket{
		peers:       make([]*kadTypes.Peer, maxBucketSize),
		peersAccess: sync.Mutex{},
	}
}

// Count the number of peers on this k-bucket
func (bucket *kBucket) CountPeers() int {
	return len(bucket.peers)
}

// Count the number of peers that can be stored yet
func (bucket *kBucket) CountRemainingPeers() int {
	return maxBucketSize - bucket.CountPeers()
}

// Check peer data and append to the end of k-bucket if correct
func (bucket *kBucket) AddPeer(newPeer *kadTypes.Peer) error {
	if newPeer == nil {
		return errors.New("kBucket only can storage not null peers")
	}

	bucket.peersAccess.Lock()

	sameNetwork := 0
	for _, peer := range bucket.peers {
		if peer.Equal(newPeer) {
			bucket.peersAccess.Unlock()
			return errors.New("kBucket already contains the passed peer")
		}
		if peer.IP().Equal(*newPeer.IP()) {
			sameNetwork++
		}
	}

	if len(bucket.peers) >= maxBucketSize {
		bucket.peersAccess.Unlock()
		return errors.New("the current kBucket is full")
	}

	if sameNetwork >= maxPeersPerIP {
		bucket.peersAccess.Unlock()
		return errors.New("many peers for the current IP")
	}

	bucket.peers = append(bucket.peers, newPeer)
	bucket.peersAccess.Unlock()
	// TODO: Adjust global tracking
	return nil
}

// Remove a peer from the bucket
func (bucket *kBucket) RemovePeer(peer *kadTypes.Peer) error {
	bucket.peersAccess.Lock()

	for index, peertr := range bucket.peers {
		if peertr.Equal(peer) {
			bucket.peers = append(bucket.peers[0:index], bucket.peers[index+1:len(bucket.peers)]...)
			bucket.peersAccess.Unlock()
			return nil
		}
	}

	bucket.peersAccess.Unlock()
	return errors.New("kBucket don't contains a peer with the passed id")
}

// Get a peer from his id
func (bucket *kBucket) GetPeer(id *types.UInt128) (*kadTypes.Peer, error) {
	bucket.peersAccess.Lock()

	for _, peer := range bucket.peers {
		peerId := peer.Id()

		if peerId.Equal(id) {
			bucket.peersAccess.Unlock()
			return peer, nil
		}
	}

	bucket.peersAccess.Unlock()
	return nil, errors.New("kBucket don't contains a peer with the passed id")
}

// Get a peer from his ip
func (bucket *kBucket) GetPeerByAddr(addr net.Addr) (*kadTypes.Peer, error) {
	bucket.peersAccess.Lock()

	var ip net.IP
	var port uint16
	var isTcp bool

	switch pAddr := addr.(type) {
	case *net.UDPAddr:
		ip = pAddr.IP
		port = uint16(pAddr.Port)
		isTcp = false
	case *net.TCPAddr:
		ip = pAddr.IP
		port = uint16(pAddr.Port)
		isTcp = true
	default:
		return nil, errors.New("incompatible network type")
	}

	for _, peer := range bucket.peers {
		if peer.IP().Equal(ip) {
			if (isTcp && peer.TCPPort() == port) || (!isTcp && peer.UDPPort() == port) {
				bucket.peersAccess.Unlock()
				return peer, nil
			}
		}
	}

	bucket.peersAccess.Unlock()
	return nil, errors.New("kBucket don't contains a peer with the passed ip")
}

func (bucket *kBucket) GetRandomPeer() (*kadTypes.Peer, error) {
	b := make([]byte, 1)
	_, err := rand.Read(b)

	if err != nil {
		return nil, err
	}

	bucket.peersAccess.Lock()

	if len(bucket.peers) > 0 {
		peer := bucket.peers[int(b[0]) % len(bucket.peers)]
		bucket.peersAccess.Unlock()
		return peer, nil
	} else {
		bucket.peersAccess.Unlock()
		return nil, errors.New("kBucket don't contains any peer")
	}
}

// Return the oldest peer in the bucket or null if not exists
func (bucket *kBucket) OldestPeer() *kadTypes.Peer {
	if len(bucket.peers) > 0 {
		return bucket.peers[0]
	} else {
		return nil
	}
}

func (bucket *kBucket) ContainsPeer(id *types.UInt128) bool {
	peer, err := bucket.GetPeer(id)
	return peer != nil && err == nil
}

func (bucket *kBucket) Peers() []*kadTypes.Peer {
	peerCpy := make([]*kadTypes.Peer, len(bucket.peers))
	copy(peerCpy, bucket.peers)
	return peerCpy
}

// Get the closest [max] peers respect the [to] id.
func (bucket *kBucket) GetClosestPeers(to *types.UInt128, max int) []*kadTypes.Peer {
	if len(bucket.peers) > 0 {
		// Filter to leave only the active peers
		peerCpy := kadTypes.Filter(bucket.Peers(), func(peer *kadTypes.Peer) bool {
			return peer.IsIpVerified() && peer.IsAlive()
		})

		// Sort by distance
		sort.Slice(peerCpy, func(i int, j int) bool {
			iDis := types.Xor(peerCpy[i].Id(), to)
			jDis := types.Xor(peerCpy[j].Id(), to)
			return iDis.Compare(jDis) < 0
		})

		// Get the [max] remaining peers
		if len(peerCpy) > max {
			return peerCpy[0:max]
		} else {
			return peerCpy
		}
	} else {
		return []*kadTypes.Peer{}
	}
}

func (bucket *kBucket) IsFull() bool {
	return bucket.CountPeers() == maxBucketSize
}

// Push the peer to the end of bucket (only if already exists)
func (bucket *kBucket) pushToEnd(peer *kadTypes.Peer) error {
	bucket.peersAccess.Lock()

	for position, currPeer := range bucket.peers {
		if peer.Equal(currPeer) {
			bucket.peers = append(append(bucket.peers[0:position], bucket.peers[position+1:len(bucket.peers)]...), peer)
			bucket.peersAccess.Unlock()
			return nil
		}
	}

	bucket.peersAccess.Unlock()
	return errors.New("kBucket don't contains the passed peer")
}

// Update last viewed status of peer, recalculate and set TTL
func (bucket *kBucket) SetPeerAlive(id *types.UInt128) error {
	inPeer, err := bucket.GetPeer(id)
	if err != nil {
		return err
	}

	inPeer.UpdateType()
	return bucket.pushToEnd(inPeer)
}
