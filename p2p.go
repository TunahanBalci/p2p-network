package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	discovery "github.com/libp2p/go-libp2p/p2p/discovery/util"
)

var (
	DiscoveryInterval   = time.Hour
	DiscoveryServiceTag = "p2p-chat-global-MAddXmr6n8u79biNV9hH3CTTmipn42F4gKCfmgGHC3wxmDtK3XQnuxFJNVnnne9N"
	DataFile            = "data.json"
	KXProtocol          = "/p2p-chat/kx/1.0.0"
)

// actual payload
type ChatMessage struct {
	ID         string `json:"id"`
	ChannelID  string `json:"channel_id"`
	SenderID   string `json:"sender_id"`
	SenderName string `json:"sender_name"`
	Content    string `json:"content"`
	Timestamp  int64  `json:"timestamp"`
}

// envolope sent over the network
type WireMessage struct {
	Type    string `json:"type"`              // "plaintext" or "encrypted"
	Payload string `json:"payload"`           // JSON(ChatMessage) OR Hex(EncryptedBlob)
	Nonce   string `json:"nonce,omitempty"`   // For encryption
}

type Channel struct {
	ID       string
	Name     string
	Topic    *pubsub.Topic
	Sub      *pubsub.Subscription
	Password string
	Messages []ChatMessage
	Unread   int
	IsDM     bool
}

type Friend struct {
	ID        string `json:"id"`
	Nick      string `json:"nick"`
	SharedKey string `json:"shared_key"` // ECDH derived secret
}

type SavedData struct {
	Nick     string   `json:"nick"`
	Friends  []Friend `json:"friends"`
	Channels []struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	} `json:"channels"`
}

type P2PNode struct {
	Host           host.Host
	PubSub         *pubsub.PubSub
	Ctx            context.Context
	Cancel         context.CancelFunc
	Channels       map[string]*Channel
	Friends        map[string]Friend
	StableID       string // deterministic
	EphemeralID    string // random per session
	Nick           string
	ChannelUpdates chan string
	Peers          chan int
	PeerCache      map[string]peer.ID
	Mutex          sync.RWMutex
}

func NewP2PNode(nick string) (*P2PNode, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// ephemeral identity (for host / network Layer)
	ephemeralPriv, _, err := crypto.GenerateKeyPairWithReader(crypto.Ed25519, 2048, rand.Reader)
	if err != nil {
		cancel()
		return nil, err
	}
	
	// create host with ephemeral Identity
	h, err := libp2p.New(
		libp2p.Identity(ephemeralPriv),
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0", "/ip4/0.0.0.0/udp/0/quic-v1"),
		libp2p.NATPortMap(), // Enable UPnP/NAT-PMP
		libp2p.EnableNATService(), // Enable AutoNAT
	)
	if err != nil {
		cancel()
		return nil, err
	}

	// stable identity (for application layer / persistence)
	stableID, err := getStableIdentity()
	if err != nil {
		cancel()
		return nil, err
	}

	// pubsub
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		cancel()
		return nil, err
	}

	// ephemeral ID is the host ID (peerID)
	ephemeralID := h.ID().ShortString()

	return &P2PNode{
		Host:           h,
		PubSub:         ps,
		Ctx:            ctx,
		Cancel:         cancel,
		Channels:       make(map[string]*Channel),
		Friends:        make(map[string]Friend),
		StableID:       stableID,
		EphemeralID:    ephemeralID,
		Nick:           nick,
		ChannelUpdates: make(chan string, 100),
		Peers:          make(chan int, 10),
		PeerCache:      make(map[string]peer.ID),
	}, nil
}

func (n *P2PNode) Start() error {
	n.Host.SetStreamHandler(protocol.ID(KXProtocol), n.handleKX)

	if err := n.setupMDNS(); err != nil {
		return err
	}
	if err := n.setupDHT(); err != nil {
		return err
	}
	go n.peerCountLoop()
	n.LoadData()
	return nil
}

func (n *P2PNode) Close() {
	n.SaveData()
	n.Cancel()
	n.Mutex.Lock()
	defer n.Mutex.Unlock()
	for _, ch := range n.Channels {
		ch.Sub.Cancel()
		ch.Topic.Close()
	}
	n.Host.Close()
}

func (n *P2PNode) JoinChannel(channelID, password string, isDM bool) error {
	n.Mutex.Lock()
	defer n.Mutex.Unlock()

	if _, exists := n.Channels[channelID]; exists {
		return nil // already joined
	}

	topic, err := n.PubSub.Join(channelID)
	if err != nil {
		return err
	}

	sub, err := topic.Subscribe()
	if err != nil {
		topic.Close()
		return err
	}

	ch := &Channel{
		ID:       channelID,
		Name:     channelID, // default name is id
		Topic:    topic,
		Sub:      sub,
		Password: password,
		Messages: []ChatMessage{},
		IsDM:     isDM,
	}

	if channelID == "global-chat" {
		ch.Name = "Global Chat"
	} else if channelID == "local-chat" {
		ch.Name = "Local Chat"
	}

	n.Channels[channelID] = ch

	go n.readLoop(ch)
	
	// save data if not DM (DMs are saved via Friends logic implicitly or we can save them explicitly)
	// for now, let's save all channels
	go n.SaveData()

	return nil
}

func (n *P2PNode) LeaveChannel(channelID string) {
	n.Mutex.Lock()
	defer n.Mutex.Unlock()

	if ch, exists := n.Channels[channelID]; exists {
		ch.Sub.Cancel()
		ch.Topic.Close()
		delete(n.Channels, channelID)
	}
	go n.SaveData()
}

func (n *P2PNode) SendMessage(channelID, content string) error {
	n.Mutex.RLock()
	ch, exists := n.Channels[channelID]
	n.Mutex.RUnlock()

	if !exists {
		return errors.New("channel not found")
	}

	// determine sender ID based on channel
	senderID := n.StableID
	if channelID == "local-chat" {
		senderID = n.EphemeralID
	}

	chatMsg := ChatMessage{
		ID:         uuid.New().String(),
		ChannelID:  channelID,
		SenderID:   senderID,
		SenderName: n.Nick,
		Content:    content,
		Timestamp:  time.Now().Unix(),
	}

	chatMsgBytes, err := json.Marshal(chatMsg)
	if err != nil {
		return err
	}

	var wireMsg WireMessage

	if ch.Password != "" {
		// encrypt the WHOLE payload
		encrypted, nonce, err := encrypt(string(chatMsgBytes), ch.Password)
		if err != nil {
			return err
		}
		wireMsg = WireMessage{
			Type:    "encrypted",
			Payload: encrypted,
			Nonce:   nonce,
		}
	} else {
		// plaintext
		wireMsg = WireMessage{
			Type:    "plaintext",
			Payload: string(chatMsgBytes),
		}
	}

	wireBytes, err := json.Marshal(wireMsg)
	if err != nil {
		return err
	}

	return ch.Topic.Publish(n.Ctx, wireBytes)
}

func (n *P2PNode) readLoop(ch *Channel) {
	for {
		msg, err := ch.Sub.Next(n.Ctx)
		if err != nil {
			return
		}

		if msg.ReceivedFrom == n.Host.ID() {
			continue
		}

		var wireMsg WireMessage
		if err := json.Unmarshal(msg.Data, &wireMsg); err != nil {
			continue
		}

		var chatMsg ChatMessage
		var payloadBytes []byte

		if wireMsg.Type == "encrypted" {
			if ch.Password == "" {
				continue
			}

			decrypted, err := decrypt(wireMsg.Payload, wireMsg.Nonce, ch.Password)
			if err != nil {
				continue
			}
			payloadBytes = []byte(decrypted)
		} else {
			payloadBytes = []byte(wireMsg.Payload)
		}

		if err := json.Unmarshal(payloadBytes, &chatMsg); err != nil {
			continue
		}

		n.Mutex.Lock()
		// Update Peer Cache
		n.PeerCache[chatMsg.SenderID] = msg.ReceivedFrom
		
		ch.Messages = append(ch.Messages, chatMsg)
		ch.Unread++
		n.Mutex.Unlock()

		n.ChannelUpdates <- ch.ID
	}
}

func (n *P2PNode) peerCountLoop() {
	ticker := time.NewTicker(time.Second * 5)
	defer ticker.Stop()

	for {
		select {
		case <-n.Ctx.Done():
			return
		case <-ticker.C:
			n.Peers <- 0 
		}
	}
}

// friend & DM Logic
func (n *P2PNode) AddFriend(id, nick string) error {
	n.Mutex.Lock()
	defer n.Mutex.Unlock()
	
	if id == n.StableID {
		return errors.New("cannot add yourself as a friend")
	}
	
	n.Friends[id] = Friend{ID: id, Nick: nick}
	go n.SaveData()
	return nil
}

func (n *P2PNode) RemoveFriend(id string) {
	n.Mutex.Lock()
	defer n.Mutex.Unlock()
	delete(n.Friends, id)
	go n.SaveData()
}

func (n *P2PNode) GetDMChannelID(peerID string) string {
	ids := []string{n.StableID, peerID}
	sort.Strings(ids)
	raw := fmt.Sprintf("dm-%s-%s", ids[0], ids[1])
	hash := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(hash[:])
}

// JoinDM now handles Key Exchange
func (n *P2PNode) JoinDM(peerID string) error {
	n.Mutex.RLock()
	friend, ok := n.Friends[peerID]
	n.Mutex.RUnlock()

	if !ok {
		return errors.New("friend not found")
	}

	if friend.SharedKey == "" {
		// Initiate Key Exchange
		key, err := n.initiateKeyExchange(peerID)
		if err != nil {
			return fmt.Errorf("handshake failed: %v", err)
		}
		
		n.Mutex.Lock()
		friend.SharedKey = key
		n.Friends[peerID] = friend
		n.Mutex.Unlock()
		
		go n.SaveData()
	}

	channelID := n.GetDMChannelID(peerID)
	// Use the SharedKey as the channel password
	return n.JoinChannel(channelID, friend.SharedKey, true)
}

// KX Logic
func (n *P2PNode) handleKX(s network.Stream) {
	defer s.Close()
	
	// 1. Generate Ephemeral Key
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return
	}
	pub := priv.PublicKey().Bytes()

	// 2. Send Public Key
	if _, err := s.Write(pub); err != nil {
		return
	}

	// 3. Read Remote Public Key
	remotePubBytes := make([]byte, 32)
	if _, err := io.ReadFull(s, remotePubBytes); err != nil {
		return
	}

	remotePub, err := ecdh.X25519().NewPublicKey(remotePubBytes)
	if err != nil {
		return
	}

	// 4. Compute Secret
	secret, err := priv.ECDH(remotePub)
	if err != nil {
		return
	}
	
	sharedKey := hex.EncodeToString(secret)
	
	// We need to know WHO this is.
	// In a real app, we'd exchange identities inside the stream too.
	// For now, we assume the stream is authenticated by LibP2P PeerID, 
	// but our App uses UUIDs.
	// Let's exchange UUIDs too.
	
	// Send UUID
	if _, err := s.Write([]byte(n.StableID)); err != nil {
		return
	}
	
	// Read UUID (expect 36 bytes)
	uuidBytes := make([]byte, 36)
	if _, err := io.ReadFull(s, uuidBytes); err != nil {
		return
	}
	remoteUUID := string(uuidBytes)

	// Save Key
	n.Mutex.Lock()
	if f, ok := n.Friends[remoteUUID]; ok {
		f.SharedKey = sharedKey
		n.Friends[remoteUUID] = f
	} else {
		// Auto-add friend? Or just ignore?
		// Let's auto-add as unknown for now to allow chat
		n.Friends[remoteUUID] = Friend{ID: remoteUUID, Nick: "Unknown", SharedKey: sharedKey}
	}
	n.Mutex.Unlock()
	
	// Auto-Join the DM channel so we can receive messages immediately
	dmID := n.GetDMChannelID(remoteUUID)
	n.JoinChannel(dmID, sharedKey, true)
	
	go n.SaveData()
}

func (n *P2PNode) initiateKeyExchange(targetUUID string) (string, error) {
	// We need to find the PeerID for this UUID.
	// Since we don't have a global UUID registry, we rely on mDNS/DHT discovery.
	// BUT, we only discover PeerIDs.
	// We need to broadcast/advertise our UUID?
	// OR, we just try to connect to all known peers and ask "Are you UUID X?"
	// This is inefficient.
	// Ideally, we'd store the PeerID in the Friend struct when we add them.
	// But the user said "add user... their uuid is saved".
	// For this implementation, let's assume we can't easily map UUID -> PeerID without a registry.
	// WORKAROUND: We will skip the PeerID lookup and assume we are connected to everyone in the mesh (small scale).
	// We will iterate over ALL connected peers and try to handshake? No, that's spammy.
	
	// Better approach for this constraint:
	// We can't easily do direct stream without PeerID.
	// We will use PubSub to signal "I want to chat with UUID X".
	// But that leaks metadata.
	
	// Let's assume for the sake of the assignment that "Adding a Friend" implies we somehow know their PeerID?
	// No, the UI only asks for UUID.
	
	// Okay, let's use the DHT to store UUID -> PeerID mapping?
	// Or simply: When we see a message in Global Chat, we capture the mapping!
	// Yes, let's add a `PeerID` field to `ChatMessage` (optional) or just capture it from the `msg.ReceivedFrom`.
	// But `ChatMessage` has `SenderID` (UUID).
	// So we can build a cache: UUID -> PeerID.
	
	// Let's add a PeerID cache to P2PNode.
	// For now, I will implement a brute-force search if not found (ask all peers).
	// Actually, let's just fail if we haven't seen them.
	
	// Wait, `handleKX` exchanges UUIDs. So if THEY initiate, we are good.
	// If WE initiate, we need their PeerID.
	
	// Let's assume we can't do it easily.
	// ALTERNATIVE: Use the "obscure" DM channel to do the handshake?
	// 1. Join `dm-UUID1-UUID2`.
	// 2. Send "HANDSHAKE_INIT" message (plaintext).
	// 3. This is visible to eavesdroppers but they can't MITM easily if we verify identities?
	// No, that's bad.
	
	// Let's go with: We need to find the peer.
	// I will add a `LookupPeer(uuid)` method that queries the DHT or checks local cache.
	// Since we don't have a custom DHT protocol, we'll rely on:
	// When we receive a message in Global Chat, we map SenderID -> msg.ReceivedFrom.
	
	peerID, found := n.findPeerID(targetUUID)
	if !found {
		return "", errors.New("peer not found (wait for them to say something in global chat)")
	}
	
	s, err := n.Host.NewStream(n.Ctx, peerID, protocol.ID(KXProtocol))
	if err != nil {
		return "", err
	}
	defer s.Close()

	// 1. Generate Ephemeral Key
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	pub := priv.PublicKey().Bytes()

	// 2. Send Public Key
	if _, err := s.Write(pub); err != nil {
		return "", err
	}

	// 3. Read Remote Public Key
	remotePubBytes := make([]byte, 32)
	if _, err := io.ReadFull(s, remotePubBytes); err != nil {
		return "", err
	}

	remotePub, err := ecdh.X25519().NewPublicKey(remotePubBytes)
	if err != nil {
		return "", err
	}

	// 4. Compute Secret
	secret, err := priv.ECDH(remotePub)
	if err != nil {
		return "", err
	}
	
	// Exchange UUIDs
	if _, err := s.Write([]byte(n.StableID)); err != nil {
		return "", err
	}
	
	uuidBytes := make([]byte, 36)
	if _, err := io.ReadFull(s, uuidBytes); err != nil {
		return "", err
	}
	remoteUUID := string(uuidBytes)
	
	if remoteUUID != targetUUID {
		return "", errors.New("UUID mismatch during handshake")
	}

	return hex.EncodeToString(secret), nil
}

func (n *P2PNode) findPeerID(targetUUID string) (peer.ID, bool) {
	n.Mutex.RLock()
	defer n.Mutex.RUnlock()
	
	id, ok := n.PeerCache[targetUUID]
	return id, ok
}

// persistence
func (n *P2PNode) SaveData() {
	n.Mutex.RLock()
	defer n.Mutex.RUnlock()

	data := SavedData{
		Nick:     n.Nick,
		Friends:  make([]Friend, 0, len(n.Friends)),
		Channels: make([]struct {
			ID       string `json:"id"`
			Password string `json:"password"`
		}, 0, len(n.Channels)),
	}

	for _, f := range n.Friends {
		data.Friends = append(data.Friends, f)
	}
	for _, ch := range n.Channels {
		if ch.ID == "global-chat" || ch.ID == "local-chat" {
			continue
		}
		data.Channels = append(data.Channels, struct {
			ID       string `json:"id"`
			Password string `json:"password"`
		}{ID: ch.ID, Password: ch.Password})
	}

	bytes, _ := json.MarshalIndent(data, "", "  ")
	
	// Encrypt data.json
	// Use SHA256(StableID) as key
	key := sha256.Sum256([]byte(n.StableID))
	encrypted, nonce, err := encrypt(string(bytes), hex.EncodeToString(key[:]))
	if err == nil {
		// Store as nonce:ciphertext
		payload := fmt.Sprintf("%s:%s", nonce, encrypted)
		_ = ioutil.WriteFile(DataFile, []byte(payload), 0644)
	}
}

func (n *P2PNode) LoadData() {
	bytes, err := ioutil.ReadFile(DataFile)
	if err != nil {
		return
	}
	
	payload := string(bytes)
	parts := strings.Split(payload, ":")
	if len(parts) != 2 {
		// Try legacy load (plaintext) for backward compatibility?
		// Or just fail. Let's try to unmarshal directly just in case it's old format.
		var data SavedData
		if err := json.Unmarshal(bytes, &data); err == nil {
			n.loadFromStruct(data)
			return
		}
		return
	}
	
	nonce := parts[0]
	ciphertext := parts[1]
	
	key := sha256.Sum256([]byte(n.StableID))
	decrypted, err := decrypt(ciphertext, nonce, hex.EncodeToString(key[:]))
	if err != nil {
		fmt.Printf("Failed to decrypt data: %v\n", err)
		return
	}
	
	var data SavedData
	if err := json.Unmarshal([]byte(decrypted), &data); err != nil {
		return
	}
	
	n.loadFromStruct(data)
}

func (n *P2PNode) loadFromStruct(data SavedData) {
	n.Mutex.Lock()
	defer n.Mutex.Unlock()
	
	if data.Nick != "" {
		n.Nick = data.Nick
	}
	
	for _, f := range data.Friends {
		n.Friends[f.ID] = f
	}
	
	for _, ch := range data.Channels {
		// We don't want to overwrite active channels if we are already running?
		// But LoadData is called at Start().
		// We need to re-join these channels.
		// But we can't call JoinChannel here because it might block or use PubSub which is ready.
		// We just populate the map? No, we need to Join them.
		// But JoinChannel calls SaveData, causing loop?
		// No, JoinChannel checks if exists.
		
		// We should just store them in a list and let the caller join?
		// Or just spawn go routines?
		// Let's just spawn go routines to join them.
		go n.JoinChannel(ch.ID, ch.Password, false)
	}
}

// identity helpers
func getStableIdentity() (string, error) {
	if _, err := os.Stat("identity.key"); err == nil {
		data, err := ioutil.ReadFile("identity.key")
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	id := uuid.New().String()
	if err := ioutil.WriteFile("identity.key", []byte(id), 0600); err != nil {
		return "", err
	}
	return id, nil
}

// encryption helpers
func encrypt(plaintext, password string) (string, string, error) {
	key := sha256.Sum256([]byte(password))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return hex.EncodeToString(ciphertext), hex.EncodeToString(nonce), nil
}

func decrypt(ciphertextHex, nonceHex, password string) (string, error) {
	key := sha256.Sum256([]byte(password))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ciphertext, err := hex.DecodeString(ciphertextHex)
	if err != nil {
		return "", err
	}
	nonce, err := hex.DecodeString(nonceHex)
	if err != nil {
		return "", err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// MDNS & DHT
type discoveryNotifee struct {
	h host.Host
}

func (d *discoveryNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == d.h.ID() {
		return
	}
	d.h.Connect(context.Background(), pi)
}

func (n *P2PNode) setupMDNS() error {
	s := mdns.NewMdnsService(n.Host, DiscoveryServiceTag, &discoveryNotifee{h: n.Host})
	return s.Start()
}

func (n *P2PNode) setupDHT() error {
	kademliaDHT, err := dht.New(n.Ctx, n.Host)
	if err != nil {
		return err
	}
	if err = kademliaDHT.Bootstrap(n.Ctx); err != nil {
		return err
	}
	var wg sync.WaitGroup
	for _, peerAddr := range dht.DefaultBootstrapPeers {
		peerinfo, _ := peer.AddrInfoFromP2pAddr(peerAddr)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.Host.Connect(n.Ctx, *peerinfo); err != nil {
			}
		}()
	}
	wg.Wait()
	routingDiscovery := routing.NewRoutingDiscovery(kademliaDHT)
	discovery.Advertise(n.Ctx, routingDiscovery, DiscoveryServiceTag)
	go func() {
		for {
			peerChan, err := routingDiscovery.FindPeers(n.Ctx, DiscoveryServiceTag)
			if err != nil {
				time.Sleep(time.Second * 10)
				continue
			}
			for peer := range peerChan {
				if peer.ID == n.Host.ID() {
					continue
				}
				if n.Host.Network().Connectedness(peer.ID) != network.Connected {
					n.Host.Connect(n.Ctx, peer)
				}
			}
			time.Sleep(time.Minute)
		}
	}()
	return nil
}
