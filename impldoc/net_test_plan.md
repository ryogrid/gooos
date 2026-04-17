# Networking Stack — Test Plan

Comprehensive verification strategy for the gooos networking
stack across all four phases: e1000 driver, Ethernet+ARP,
IPv4+ICMP+UDP, and robustness/diagnostics.

Parent doc: `net_overview.md`.

---

## 1. Test Environment

### 1.1 QEMU Configurations

All tests run on QEMU x86-64 with the kernel loaded via
`-kernel` or `-cdrom`.

**Configuration A — User-mode networking (no root):**
```bash
qemu-system-x86_64 -kernel tmp/kernel.bin -serial stdio \
  -device e1000,netdev=n0 \
  -netdev user,id=n0 \
  -no-reboot -no-shutdown
```
- Guest IP: 10.0.2.15 (QEMU default)
- Gateway: 10.0.2.2
- Limited: host cannot initiate connections to guest
- Useful for: TX tests, ARP learning from QEMU-initiated traffic

**Configuration B — User-mode with port forwarding:**
```bash
qemu-system-x86_64 -kernel tmp/kernel.bin -serial stdio \
  -device e1000,netdev=n0 \
  -netdev user,id=n0,hostfwd=udp::9999-:7 \
  -no-reboot -no-shutdown
```
- Guest port 7 (UDP echo) forwarded to host port 9999
- Useful for: UDP echo tests from host

**Configuration C — TAP networking (requires root):**
```bash
# Setup (run once as root):
ip tuntap add dev tap0 mode tap
ip addr add 10.0.0.1/24 dev tap0
ip link set tap0 up

# QEMU:
qemu-system-x86_64 -kernel tmp/kernel.bin -serial stdio \
  -device e1000,netdev=n0 \
  -netdev tap,id=n0,ifname=tap0,script=no,downscript=no \
  -no-reboot -no-shutdown
```
- Guest IP: 10.0.0.2 (configured in kernel)
- Host can ping guest directly
- Useful for: ICMP ping tests, bidirectional UDP

**Configuration D — pcap capture (any mode):**
Add `dump=file:net.pcap` to `-netdev`:
```bash
-netdev user,id=n0,dump=file:net.pcap
```
- Captures all packets to/from the NIC
- Analyze with Wireshark or `tcpdump -r net.pcap`

### 1.2 Makefile Targets

Proposed additions to `Makefile`:
```makefile
run-net: $(KERNEL_BIN) check-multiboot
	$(QEMU) -kernel $(KERNEL_BIN) -serial stdio \
	  -no-reboot -no-shutdown \
	  -device e1000,netdev=n0 \
	  -netdev user,id=n0

run-net-tap: $(KERNEL_BIN) check-multiboot
	$(QEMU) -kernel $(KERNEL_BIN) -serial stdio \
	  -no-reboot -no-shutdown \
	  -device e1000,netdev=n0 \
	  -netdev tap,id=n0,ifname=tap0,script=no,downscript=no

run-net-pcap: $(KERNEL_BIN) check-multiboot
	$(QEMU) -kernel $(KERNEL_BIN) -serial stdio \
	  -no-reboot -no-shutdown \
	  -device e1000,netdev=n0 \
	  -netdev user,id=n0,dump=file:net.pcap
```

### 1.3 Serial Log Verification

All tests use serial output as the primary verification
channel. Tests are automated by grep-ing serial output:

```bash
# Run kernel with timeout, capture serial output:
timeout 30 qemu-system-x86_64 -kernel tmp/kernel.bin \
  -serial stdio -device e1000,netdev=n0 \
  -netdev user,id=n0 -no-reboot -no-shutdown \
  2>/dev/null | tee serial.log

# Check for expected strings:
grep "PCI: found e1000" serial.log
grep "e1000: link up" serial.log
grep "NET: initialized" serial.log
```

---

## 2. Phase 1 Tests — e1000 NIC Driver

### T1.1 PCI Device Discovery

| Item | Detail |
|---|---|
| **Precondition** | QEMU invoked with `-device e1000` |
| **Action** | Boot kernel |
| **Expected serial** | `PCI: found e1000 at 00:03.0 BAR0=0xFEBC0000 IRQ=11` (exact bus/device may vary) |
| **Failure** | `e1000: not found on PCI bus` → check PCI scan range and vendor/device ID matching |

### T1.2 MMIO Register Read

| Item | Detail |
|---|---|
| **Precondition** | e1000 found, BAR0 mapped |
| **Action** | Read `e1000STATUS` register |
| **Expected serial** | `e1000: STATUS=0x...` (non-zero value; bit 1 = link up) |
| **Failure** | STATUS = 0 → MMIO mapping failed (check PCD/PWT flags, BAR0 address) |

### T1.3 MAC Address Read

| Item | Detail |
|---|---|
| **Precondition** | e1000 initialized |
| **Action** | Read RAL0/RAH0 |
| **Expected serial** | `e1000: MAC=52:54:00:12:34:56` (QEMU default MAC varies) |
| **Failure** | MAC = 00:00:00:00:00:00 → RAL0/RAH0 read before init, or EEPROM not loaded |

### T1.4 Link Up

| Item | Detail |
|---|---|
| **Precondition** | e1000 initialized, CTRL.SLU set |
| **Action** | Poll STATUS.LU with timeout |
| **Expected serial** | `e1000: link up` (within 5 seconds) |
| **Failure** | `e1000: link up timeout` → check CTRL.SLU, CTRL.ASDE |

### T1.5 TX Broadcast Frame

| Item | Detail |
|---|---|
| **Precondition** | e1000 link up |
| **Action** | Transmit 64-byte broadcast frame (dst=FF:FF:FF:FF:FF:FF) with payload "GOOOS TX TEST" |
| **Expected** | Packet visible in pcap dump (Config D) |
| **Verification** | `tcpdump -r net.pcap -XX | grep "GOOOS TX TEST"` |

### T1.6 RX First Packet

| Item | Detail |
|---|---|
| **Precondition** | e1000 initialized, RCTL enabled |
| **Action** | QEMU user-mode sends ARP requests automatically |
| **Expected serial** | `e1000: RX packet len=N` (first received packet) |
| **Verification** | `N` should be 42-60 bytes (ARP is 42 bytes minimum) |

### T1.7 IRQ Delivery

| Item | Detail |
|---|---|
| **Precondition** | e1000 IMS set with RXT0 bit |
| **Action** | Receive a packet |
| **Expected serial** | `e1000: IRQ vector=43 ICR=0x...` |
| **Failure** | No IRQ → check PIC masking, IRQ line, handler registration |

### T1.8 Regression: Boot Completes

| Item | Detail |
|---|---|
| **Precondition** | `-device e1000` added |
| **Action** | Full boot sequence |
| **Expected** | Shell prompt appears on VGA; all existing serial log messages present |
| **Verification** | `grep "Scheduler: TinyGo goroutines active" serial.log` |

---

## 3. Phase 2 Tests — Ethernet + ARP

### T2.1 ARP Reply to Host

| Item | Detail |
|---|---|
| **Precondition** | Kernel booted, netInit complete |
| **Action** | Host sends ARP request for kernel IP (Config C: `arping -I tap0 10.0.0.2`) |
| **Expected serial** | `ARP: learned 10.0.0.1 = XX:XX:XX:XX:XX:XX` |
| **Expected pcap** | ARP request from host, ARP reply from kernel |

### T2.2 Gratuitous ARP on Boot

| Item | Detail |
|---|---|
| **Precondition** | Kernel boots with network |
| **Action** | Observe boot serial output |
| **Expected serial** | `ARP: sent gratuitous ARP` |
| **Expected pcap** | ARP reply with SPA=TPA=10.0.2.15, broadcast destination |

### T2.3 ARP Cache Population

| Item | Detail |
|---|---|
| **Precondition** | After several ARP exchanges |
| **Action** | Call `netDiag()` or inspect serial log |
| **Expected** | ARP cache shows gateway IP → MAC mapping |

### T2.4 ARP Timeout

| Item | Detail |
|---|---|
| **Precondition** | Network active |
| **Action** | Kernel calls `arpResolve(10.0.2.100)` (IP does not exist) |
| **Expected** | Returns false after ~2 seconds |
| **Expected serial** | `ARP: resolve timeout for 10.0.2.100` |
| **Counter** | `netStats.ArpMisses` incremented |

### T2.5 Unknown EtherType Drop

| Item | Detail |
|---|---|
| **Precondition** | Network active |
| **Action** | QEMU may send IPv6 (EtherType 0x86DD) |
| **Expected** | Packet dropped silently |
| **Counter** | `netStats.RxUnknownEtherType` incremented |

---

## 4. Phase 3 Tests — IPv4 + ICMP + UDP

### T3.1 ICMP Ping Reply (TAP mode)

| Item | Detail |
|---|---|
| **Precondition** | Config C (TAP), kernel IP=10.0.0.2 |
| **Action** | `ping -c 5 10.0.0.2` from host |
| **Expected** | 5 replies received, RTT < 10 ms |
| **Expected serial** | `ICMP: echo reply to 10.0.0.1` × 5 |
| **Counter** | `netStats.IcmpEcho` = 5 |

### T3.2 ICMP Ping Reply (User-mode)

| Item | Detail |
|---|---|
| **Precondition** | Config A, kernel IP=10.0.2.15 |
| **Action** | QEMU gateway (10.0.2.2) may send ICMP if configured |
| **Expected** | Any ICMP echo request generates a reply |
| **Note** | User-mode networking may not generate ICMP to guest; TAP preferred for this test |

### T3.3 UDP Echo (Port Forwarding)

| Item | Detail |
|---|---|
| **Precondition** | Config B (`hostfwd=udp::9999-:7`), echo server on port 7 |
| **Action** | `echo "hello" | nc -u -w1 127.0.0.1 9999` |
| **Expected** | `nc` receives "hello" back |
| **Expected serial** | `netStats.UdpRecv` and `netStats.UdpSend` incremented |

### T3.4 UDP Echo (TAP mode)

| Item | Detail |
|---|---|
| **Precondition** | Config C, kernel IP=10.0.0.2, echo server on port 7 |
| **Action** | `echo "hello world" | nc -u -w1 10.0.0.2 7` |
| **Expected** | `nc` receives "hello world" back |

### T3.5 UDP Send (Kernel-Initiated)

| Item | Detail |
|---|---|
| **Precondition** | Config C, host listens: `nc -lu 9999` |
| **Action** | Kernel sends `udpSend(gateway, 9999, 1234, "gooos alive")` at boot |
| **Expected** | Host `nc` receives "gooos alive" |

### T3.6 IPv4 Checksum Validation

| Item | Detail |
|---|---|
| **Precondition** | Network active |
| **Action** | Use a raw socket tool (e.g., `hping3`) to send a packet with bad IPv4 checksum |
| **Expected** | Packet dropped, `netStats.ChecksumErr` incremented |
| **Alt method** | Inject known-bad packet bytes directly in a test function |

### T3.7 UDP Checksum Validation

| Item | Detail |
|---|---|
| **Same as T3.6 but for UDP checksum** |

### T3.8 Fragment Drop

| Item | Detail |
|---|---|
| **Precondition** | Network active |
| **Action** | Send an IPv4 packet with MF=1 (More Fragments flag) |
| **Expected** | Dropped, `netStats.FragmentsDropped` incremented |

### T3.9 Large UDP Payload

| Item | Detail |
|---|---|
| **Precondition** | Echo server running |
| **Action** | Send 1472-byte UDP payload (max without fragmentation) |
| **Expected** | Echo reply with same payload |

### T3.10 Oversized UDP Payload

| Item | Detail |
|---|---|
| **Precondition** | Echo server running |
| **Action** | Call `udpSend` with 1473-byte payload |
| **Expected** | `ipv4Send` returns false (MTU exceeded) |

---

## 5. Phase 4 Tests — Robustness and Diagnostics

### T4.1 Buffer Pool Lifecycle

| Item | Detail |
|---|---|
| **Precondition** | `netBufInit()` called |
| **Action** | Allocate 128 buffers, verify all succeed; allocate 129th |
| **Expected** | 128 succeed; 129th returns 0 |
| **Action** | Free buffer 0; allocate again |
| **Expected** | Succeeds with buffer 0's address |

### T4.2 Buffer Pool Under Load

| Item | Detail |
|---|---|
| **Precondition** | Network active with buffer pool |
| **Action** | Generate rapid traffic: `hping3 --udp -p 7 --flood 10.0.0.2` (TAP mode) |
| **Expected** | `BufAllocFail` counter may increment but kernel does not crash |
| **Expected** | `RxDropped` counter increments for dropped packets |

### T4.3 Interrupt-Driven RX

| Item | Detail |
|---|---|
| **Precondition** | Phase 4 IRQ-driven RX enabled |
| **Action** | `ping -c 10 10.0.0.2` (TAP mode) |
| **Expected** | All 10 replies received |
| **Verification** | No polling loop active; goroutine blocks on `<-rxSignalCh` |

### T4.4 Network Diagnostics Output

| Item | Detail |
|---|---|
| **Precondition** | Network active, some traffic exchanged |
| **Action** | Trigger `netDiag()` (after 5-second delay) |
| **Expected serial** | Complete diagnostic output: link status, MAC, IP, ARP cache, all counters |

### T4.5 Error Handling: Runt Frame

| Item | Detail |
|---|---|
| **Precondition** | Network active |
| **Action** | Receive a frame < 60 bytes (e1000 may pad, but test the check) |
| **Expected** | `RxDropped` counter incremented if frame is below minimum |

### T4.6 Regression: Full Boot + Shell

| Item | Detail |
|---|---|
| **Precondition** | All four phases implemented |
| **Action** | `make run-net` (or equivalent) |
| **Expected** | Full boot sequence completes; shell prompt appears; `ls`, `cat`, `hello` commands work |
| **Verification** | All existing test harnesses pass |

---

## 6. Automated Test Script

### 6.1 `scripts/test_net.sh`

A shell script that automates the serial log verification:

```bash
#!/bin/bash
# scripts/test_net.sh — Network smoke test (user-mode)
set -euo pipefail

KERNEL=tmp/kernel.bin
TIMEOUT=30
LOG=$(mktemp)
PASS=0
FAIL=0

echo "Starting QEMU with e1000 NIC..."
timeout ${TIMEOUT} qemu-system-x86_64 \
  -kernel ${KERNEL} -serial stdio \
  -device e1000,netdev=n0 \
  -netdev user,id=n0 \
  -no-reboot -no-shutdown -display none \
  2>/dev/null > ${LOG} &
QEMU_PID=$!

sleep ${TIMEOUT}
kill ${QEMU_PID} 2>/dev/null || true
wait ${QEMU_PID} 2>/dev/null || true

echo "Checking serial log..."

check() {
    local pattern="$1"
    local desc="$2"
    if grep -q "${pattern}" ${LOG}; then
        echo "  PASS: ${desc}"
        PASS=$((PASS + 1))
    else
        echo "  FAIL: ${desc}"
        FAIL=$((FAIL + 1))
    fi
}

check "PCI: found e1000"       "PCI device discovery"
check "e1000: MAC="            "MAC address read"
check "e1000: link up"         "Link up"
check "NET: initialized"       "Network initialized"
check "ARP: sent gratuitous"   "Gratuitous ARP"
check "UDP echo: listening"    "UDP echo server started"
check "Scheduler: TinyGo"      "Boot completed"

echo ""
echo "Results: ${PASS} passed, ${FAIL} failed"
rm -f ${LOG}
[ ${FAIL} -eq 0 ]
```

### 6.2 TAP-Based Integration Test

For full bidirectional testing (requires root/capabilities):

```bash
#!/bin/bash
# scripts/test_net_tap.sh — Network integration test (TAP)
# Requires: sudo or CAP_NET_ADMIN

# Setup TAP
sudo ip tuntap add dev tap0 mode tap
sudo ip addr add 10.0.0.1/24 dev tap0
sudo ip link set tap0 up

# Start QEMU in background
qemu-system-x86_64 -kernel tmp/kernel.bin -serial stdio \
  -device e1000,netdev=n0 \
  -netdev tap,id=n0,ifname=tap0,script=no,downscript=no \
  -no-reboot -no-shutdown -display none &
QEMU_PID=$!

# Wait for kernel to boot
sleep 15

# Test: Ping
echo "Testing ICMP ping..."
ping -c 5 -W 2 10.0.0.2 && echo "PASS: ping" || echo "FAIL: ping"

# Test: UDP echo
echo "Testing UDP echo..."
REPLY=$(echo "hello" | nc -u -w2 10.0.0.2 7)
[ "$REPLY" = "hello" ] && echo "PASS: UDP echo" || echo "FAIL: UDP echo"

# Cleanup
kill ${QEMU_PID} 2>/dev/null
sudo ip link delete tap0
```

---

## 7. Known Limitations of Test Environment

| Limitation | Impact | Workaround |
|---|---|---|
| QEMU user-mode: host cannot ping guest | T3.1 requires TAP | Use TAP mode for ping tests |
| QEMU user-mode: no raw socket injection | T3.6–T3.8 difficult | Use in-kernel test functions that call `ipv4Parse` directly with crafted byte arrays |
| TinyGo `go test` does not work for `src/` | No standard unit tests | Serial-log-based assertions in boot code; `scripts/test_net.sh` |
| Conservative GC may scan net buffer pool addresses | False positive GC roots | Buffers are outside GC heap; no correctness impact (see `net_buffers_diagnostics.md §1.2`) |
| IOAPIC disabled | IRQ routing limited to PIC | PIC pass-through works; IOAPIC deferred |

---

## 8. In-Kernel Self-Test Functions

Since `go test` cannot run on the bare-metal kernel, self-test
functions run during boot and log results to serial:

### 8.1 Checksum Self-Test

```go
func testIPv4Checksum() {
    // Known-good IPv4 header (from RFC 1071 example)
    hdr := []byte{
        0x45, 0x00, 0x00, 0x73, 0x00, 0x00, 0x40, 0x00,
        0x40, 0x11, 0xB8, 0x61, 0xC0, 0xA8, 0x00, 0x01,
        0xC0, 0xA8, 0x00, 0xC7,
    }
    if ipv4Checksum(hdr) == 0 {
        serialPrintln("TEST: IPv4 checksum PASS")
    } else {
        serialPrintln("TEST: IPv4 checksum FAIL")
    }
}
```

### 8.2 Byte-Order Self-Test

```go
func testByteOrder() {
    if htons(0x0102) == 0x0201 {
        serialPrintln("TEST: htons PASS")
    } else {
        serialPrintln("TEST: htons FAIL")
    }
    if htonl(0x01020304) == 0x04030201 {
        serialPrintln("TEST: htonl PASS")
    } else {
        serialPrintln("TEST: htonl FAIL")
    }
}
```

### 8.3 ARP Parse Self-Test

```go
func testARPParse() {
    // Known ARP request bytes
    data := []byte{
        0x00, 0x01,  // HW type: Ethernet
        0x08, 0x00,  // Proto: IPv4
        0x06,        // HW addr len
        0x04,        // Proto addr len
        0x00, 0x01,  // Op: Request
        // SHA: 52:54:00:12:34:56
        0x52, 0x54, 0x00, 0x12, 0x34, 0x56,
        // SPA: 10.0.2.2
        0x0A, 0x00, 0x02, 0x02,
        // THA: 00:00:00:00:00:00
        0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
        // TPA: 10.0.2.15
        0x0A, 0x00, 0x02, 0x0F,
    }
    pkt, ok := arpParse(data)
    if ok && pkt.Op == 1 && pkt.SPA == 0x0A000202 &&
       pkt.TPA == 0x0A00020F {
        serialPrintln("TEST: ARP parse PASS")
    } else {
        serialPrintln("TEST: ARP parse FAIL")
    }
}
```

### 8.4 Buffer Pool Self-Test

```go
func testNetBuf() {
    // Alloc and free cycle
    addr, idx := netBufAlloc()
    if addr != 0 {
        netBufFreeIdx(idx)
        serialPrintln("TEST: netbuf alloc/free PASS")
    } else {
        serialPrintln("TEST: netbuf alloc FAIL")
    }
}
```

---

## 9. Test Matrix Summary

| Test ID | Phase | Description | Config | Automated |
|---|---|---|---|---|
| T1.1 | 1 | PCI discovery | A | ✓ (serial grep) |
| T1.2 | 1 | MMIO register read | A | ✓ |
| T1.3 | 1 | MAC address read | A | ✓ |
| T1.4 | 1 | Link up | A | ✓ |
| T1.5 | 1 | TX broadcast | D | Manual (pcap) |
| T1.6 | 1 | RX first packet | A | ✓ |
| T1.7 | 1 | IRQ delivery | A | ✓ |
| T1.8 | 1 | Boot regression | A | ✓ |
| T2.1 | 2 | ARP reply | C | Manual |
| T2.2 | 2 | Gratuitous ARP | A | ✓ |
| T2.3 | 2 | ARP cache | A | ✓ |
| T2.4 | 2 | ARP timeout | — | In-kernel self-test |
| T2.5 | 2 | Unknown EtherType | A | ✓ (counter) |
| T3.1 | 3 | ICMP ping (TAP) | C | ✓ (`test_net_tap.sh`) |
| T3.3 | 3 | UDP echo (fwd) | B | ✓ (`test_net_tap.sh`) |
| T3.4 | 3 | UDP echo (TAP) | C | ✓ |
| T3.5 | 3 | UDP send | C | ✓ |
| T3.6 | 3 | Bad checksum | — | In-kernel self-test |
| T3.8 | 3 | Fragment drop | — | In-kernel self-test |
| T3.9 | 3 | Max UDP payload | C | Manual |
| T4.1 | 4 | Buffer pool | — | In-kernel self-test |
| T4.2 | 4 | Flood test | C | Manual (`hping3`) |
| T4.3 | 4 | IRQ-driven RX | C | ✓ |
| T4.4 | 4 | Diagnostics | A | ✓ |
| T4.6 | 4 | Boot regression | A | ✓ |

---

## 10. Success Criteria

The networking stack is considered complete when:

1. **Phase 1**: e1000 detected, link up, TX/RX work (serial
   log + pcap verification).
2. **Phase 2**: ARP exchange works (host can resolve kernel's
   IP to MAC).
3. **Phase 3**: `ping` replies succeed; UDP echo server works.
4. **Phase 4**: Buffer pool operates without leak; statistics
   counters are accurate; diagnostics print correctly.
5. **Regression**: All existing boot-time tests pass; shell
   commands work; `make lint` passes.
6. **Automated**: `scripts/test_net.sh` exits 0 with all
   checks passing.
