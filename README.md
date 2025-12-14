# P2P Chat Application

> A serverless and secure peer-to-peer chat application built with **Go**, **LibP2P**, and **Fyne**.

![License](https://img.shields.io/badge/license-MIT-blue.svg)
![Go Version](https://img.shields.io/badge/go-1.21%2B-cyan)
![Platform](https://img.shields.io/badge/platform-Windows%20%7C%20macOS%20%7C%20Linux-lightgrey)

---

## Overview

This application allows users to communicate directly over a local network (LAN) or the Internet (WAN) without relying on any central servers. It prioritizes **privacy**, **security**, and **ease of use**.

Whether you are in the same room or across the world, your messages travel directly from your device to your friend's device.

## Key Features

*   **Serverless Architecture**: No central authority. You own your data.
*   **End-to-End Encryption**: Private Direct Messages (DMs) are encrypted using **AES-GCM** with keys derived via **ECDH** (X25519) handshake.
*   **Privacy First**:
    *   **Topic Hashing**: Channel names are hashed so observers cannot see *who* is talking to *who*.
    *   **Local Encryption**: Your friends list and message history are encrypted at rest on your device.
*   **Universal Connectivity**:
    *   **mDNS**: Automatic zero-config discovery on Local Networks (LAN).
    *   **DHT & UPnP**: Automatic port forwarding and peer discovery over the Internet (WAN).
*   **Cross-Platform GUI**: A rich, responsive interface that works seamlessly on Windows, macOS, and Linux.
*   **Hybrid Identity**:
    *   **Stable ID**: Your permanent identity (UUID) for friends and persistence.
    *   **Ephemeral ID**: A random network ID generated every session for anonymity in public channels.

## Tech Stack

*   **Language**: [Go (Golang)](https://go.dev/)
*   **Networking**: [LibP2P](https://libp2p.io/) (The stack powering IPFS and Ethereum 2.0)
    *   **Transport**: TCP / QUIC
    *   **Routing**: GossipSub
    *   **Discovery**: Kademlia DHT + mDNS
*   **GUI**: [Fyne](https://fyne.io/) (Material Design based UI toolkit)
*   **Security**: `crypto/ecdh`, `crypto/aes`, `crypto/sha256`

---

## Architecture

### 1. The P2P Node (`p2p.go`)
The core logic runs in a background goroutine, managing the LibP2P host. It handles:
*   **Discovery**: finding peers via mDNS (local) or DHT (global).
*   **Routing**: Propagating messages using the GossipSub protocol.
*   **Handshakes**: Performing secure key exchanges for DMs.

### 2. The GUI (`gui.go`)
The frontend is decoupled from the networking layer. It communicates via:
*   **Channels**: Receiving real-time updates (messages, peer counts).
*   **Callbacks**: Triggering actions (sending messages, adding friends).
*   **State**: Optimistic UI updates for a snappy experience.

### 3. Data Persistence
*   **`data.json`**: Stores your profile, friends, and joined channels. Encrypted with your Stable ID.
*   **`identity.key`**: Stores your Stable UUID.

---

## Getting Started

### Prerequisites
*   **Go 1.21 or higher**: [Download here](https://go.dev/dl/)

### Installation

1.  **Clone the repository** (or download the source code):
    ```bash
    git clone https://github.com/yourusername/p2p-chat.git
    cd p2p-chat
    ```

2.  **Build the Application**:
    We have provided automated scripts to check dependencies and build the app.

    *   **Windows**:
        Double-click **`build.bat`**.
    
    *   **Linux /  macOS**:
        Run the shell script:
        ```bash
        chmod +x build.sh
        ./build.sh
        ```

3.  **Run**:
    *   **Windows**: `p2p-chat.exe`
    *   **Linux/macOS**: `./p2p-chat`

---

## Usage

### 1. First Launch
*   You will be prompted to set a **Nickname**.
*   The app will automatically join **Global Chat** and **Local Chat**.
*   **Firewall**: Please allow the application access to Private/Public networks when prompted.

### 2. Adding Friends
1.  Ask your friend for their **UUID** (found in their Profile section).
2.  Click **"Add Friend"** and enter their UUID and a Nickname.
3.  Click their name in the Friend List to start a **Secure DM**.
    *   *Note: The first time you message, a secure handshake occurs automatically.*

### 3. Public vs. Local Chat
*   **Global Chat**: Visible to everyone on the P2P mesh.
*   **Local Chat**: Visible only to peers on your local Wi-Fi/LAN. Messages here show your **Session ID** to distinguish devices.

### 4. Configuration (`.env`)
You can isolate your network by changing the Discovery Tag in the `.env` file:
```env
DISCOVERY_SERVICE_TAG=your-custom-message
```
Change this string to create a private mesh with only people who share the same tag. It could be any string.

---

## Security & Privacy Details

| Feature | Implementation |
| :--- | :--- |
| **Transport Security** | All connections are TLS 1.3 encrypted by LibP2P default transports. |
| **DM Encryption** | **AES-256-GCM**. Keys are never sent over the wire; they are derived locally via ECDH. |
| **Metadata Protection** | DM Channel Topics are **Hashed** (`SHA256`). The network sees `a3f9...`, not `Alice-talking-to-Bob`. |
| **Local Storage** | `data.json` is encrypted using a key derived from your Stable ID. |

---

## Troubleshooting

**I can't see my friend over the internet!**
*   **UPnP**: Ensure UPnP is enabled on your router (usually is by default).
*   **Firewall**: Check if your OS firewall is blocking the app.
*   **Wait**: DHT discovery can take a minute or two to propagate.

**I get a "Go not found" error.**
*   Ensure Go is installed and added to your system `PATH`. Restart your terminal/computer after installing Go.
