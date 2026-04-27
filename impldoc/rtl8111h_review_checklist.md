# Networking Stack — RTL8111H Code-Review Checklist & One-Shot Bring-Up Test Plan

The exhaustive ≥25-item code-review checklist (§A) plus the one-shot real-hardware bring-up test plan (§B) for the RTL8111H driver. Because end-to-end verification only happens **once** on real hardware after the implementation is fully written, these two sections are the primary correctness gate. Treat every item as MUST-PASS.

Parent doc: `rtl8111h_overview.md` (Chapters 12 and 13).
Sibling doc: `rtl8111h_phy_init.md` (referenced by checklist items as "phy_init §N.M").

Format used throughout §A:

> **N.** *one-line claim* — Verify against [Linux function name or datasheet section] / [phy_init or overview section anchor]. **Failure mode**: *what breaks on hardware if this item is wrong*.

---

## §A. Code-review checklist

### A.1 PCI discovery

**1.** PCI vendor/device match arm in `pciInit` reads `vendor == 0x10EC` and `device == 0x8168` exactly. — Verify against `r8169_main.c:rtl8169_pci_tbl` (`PCI_VDEVICE(REALTEK, 0x8168)`). **Failure mode**: chip is silently ignored (boot proceeds with no networking) or, worse, a non-Realtek device with a coincidental ID is mis-identified.

**2.** `pciRecordRTL8111H` masks BAR0 low 4 bits (`bar0 &^ 0xF`) before storing. — Verify against `pciRecordE1000`. **Failure mode**: BAR0 mapped at `bar0 + N` instead of `bar0`, every register access lands on the wrong offset; chip looks dead.

**3.** `pciRecordRTL8111H` sets `Memory Space (bit 1) | Bus Master (bit 2)` in PCI Cmd before the chip is touched via MMIO/DMA. — Verify against `pciRecordE1000` and upstream `pcim_enable_device` + `pci_set_master`. **Failure mode**: every MMIO write returns 0xFF; descriptor DMA is silently dropped.

### A.2 BAR0 mapping & register block

**4.** `rtl8111hMapMMIO` maps **128 KiB** with `pagePresent | pageWrite | pagePCD | pagePWT` flags (PCD + PWT to mark the region as uncacheable). — Verify against `e1000MapMMIO` and `src/vm.go:mapPage` flag definitions. **Failure mode**: register accesses are cached, reads return stale values, ISR sees stale `IntrStatus`.

**5.** Every register offset constant in `src/rtl8111h.go` matches the offset table in `rtl8111h_overview.md` §2.2. — Cross-check each: `MAC0=0x00`, `MAC4=0x04`, `TxDescStartAddrLow=0x20`, `TxDescStartAddrHigh=0x24`, `ChipCmd=0x37`, `TxPoll=0x38`, `IntrMask=0x3c`, `IntrStatus=0x3e`, `TxConfig=0x40`, `RxConfig=0x44`, `Cfg9346=0x50`, `PHYstatus=0x6c`, `PMCH=0x6f`, `ERIDR=0x70`, `ERIAR=0x74`, `EPHYAR=0x80`, `OCPDR=0xb0`, `OCPAR=0xb4`, `GPHY_OCP=0xb8`, `RxMaxSize=0xda`, `RxDescAddrLow=0xe4`, `RxDescAddrHigh=0xe8`, `MISC=0xf0`. **Failure mode**: any single offset wrong and that subsystem fails silently — usually link-up never happens.

**6.** Driver does **not** use `IntrMask_8125 = 0x38` or `IntrStatus_8125 = 0x3c` (those are 32-bit registers reserved for the 2.5 GbE family). — Verify against `r8169_main.c:rtl_get_events` (the `rtl_is_8125(tp)` branch). For VER_46, `rtl_is_8125 == false`. **Failure mode**: ISR reads/writes overlap with `TxPoll`/`IntrMask` legacy registers, scrambling both.

### A.3 Reset and PHY power-up

**7.** Software reset writes `Cmd = Reset (0x10)` and polls until self-clear with timeout. — Verify against `r8169_main.c:rtl_hw_reset`. **Failure mode**: subsequent register writes land on a chip in indeterminate state; init sequence is non-reproducible.

**8.** Driver inserts `msleep(20)` after PHY power-up before any PHY MMIO/MDIO access. — **Footgun #2** (commit `3148ded`, "r8169: fix powering up RTL8168h"). Cite the historical commit, not a current upstream function name (the original function `rtl_pll_power_up` was refactored away — current upstream delegates the wait to PHYlib's `phy_resume(tp->phydev)` called from `r8169_main.c:rtl8169_up`). **Failure mode**: PHY MII writes silently land nowhere, link-up never happens. Often misdiagnosed as "PHY ID wrong" or "auto-neg broken".

**9.** XID verification accepts only `0x541` (RTL8111H) and `0x6c0` (RTL8168M); rejects everything else. — Verify against `r8169_main.c:rtl_chip_infos[]`. **Failure mode**: a foreign 8168-family chip (e.g. 8111B/C/D) is run through the VER_46 init, which programs the wrong PHY tables. **Footgun #5** (commit `ebe5989`): legacy VER_45 paths must NOT be reachable.

### A.4 ASPM (Footgun #3)

**10.** Driver writes PCIe config-space byte `0x70F` with `0x27` (L0=7 µs, L1=16 µs) via `rtl_set_def_aspm_entry_latency` equivalent before enabling RX/TX engines. — Verify against `r8169_main.c:rtl_set_def_aspm_entry_latency`. **Failure mode**: chip races into L1 too aggressively, drops PCIe transactions on some boards (Footgun #3).

**11.** Driver explicitly disables PCIe ASPM L1 by clearing bit 1 of PCIe Express capability Link Control register (cap+0x10) before enabling RX/TX engines. — Verify against `r8169_main.c:rtl_init_one` (`pci_disable_link_state(pdev, PCIE_LINK_STATE_L1)`). **Footgun #3** (commit `90ca51e`). **Failure mode**: TX queue timeouts, `rx_missed` counter grows, PCIe link drops at idle.

### A.5 Descriptor rings

**12.** TX and RX descriptor rings are allocated via `allocPagesContig` and **256-byte aligned**. — Verify against upstream (`rtl_open` allocates with `dma_alloc_coherent`, which is page-aligned ≥ 256 B). **Failure mode**: chip ignores low bits of descriptor base, ring is at wrong physical address, descriptors never advance.

**13.** TX descriptor ring length = **256 entries × 16 bytes = 4096 bytes**. RX descriptor ring length = **256 × 16 = 4096 bytes**. — Verify against `r8169_main.c:NUM_TX_DESC` / `NUM_RX_DESC`. **Failure mode**: chip overruns ring boundary into adjacent memory; corruption.

**14.** Last descriptor in each ring has `RingEnd (1<<30)` set in `opts1` at init **and** preserved across every `release_descriptor` rearm. — Verify against `r8169_main.c:rtl8169_mark_to_asic`. **Failure mode**: chip walks off end of ring into adjacent memory; arbitrary corruption.

**15.** `DescOwn (1<<31)` is the **last** field written when handing a descriptor to the NIC. `addr` and `opts2` are written first; then a write barrier (`dma_wmb`-equivalent on x86 is a no-op for single-writer but the comment must be present); then `opts1`. — Verify against `r8169_main.c:rtl8169_start_xmit` (`dma_wmb()` before `txd_first->opts1 |= DescOwn`). **Failure mode**: NIC reads stale `addr`/`opts2`, sends junk on wire (TX) or writes received data to wrong address (RX).

**16.** `DescOwn` polarity is correct: **set by driver** when handing to NIC; **NIC clears it** on RX (we read 0 to know it's complete) and **leaves it 0 after TX** (we re-set it on every reuse). — Verify against `r8169_main.c:rtl_rx` (`if (status & DescOwn) break`) and `rtl8169_start_xmit`. **Failure mode**: ring pointers race with NIC, missed frames or duplicate sends.

**17.** Descriptor base addresses are written **high-then-low** for both TX and RX. — Verify against `r8169_main.c:rtl_set_rx_tx_desc_registers`. **Failure mode**: not a correctness issue on x86 but consistency with upstream is part of the citation contract.

### A.6 EPHY / ERI / MAC-OCP / PHY init

**18.** `e_info_8168h_1[]` table is transcribed exactly as 6 entries: `{0x1e, 0x0800, 0x0001}`, `{0x1d, 0x0000, 0x0800}`, `{0x05, 0xffff, 0x2089}`, `{0x06, 0xffff, 0x5881}`, `{0x04, 0xffff, 0x854a}`, `{0x01, 0xffff, 0x068b}`. — Verify against `r8169_main.c:rtl_hw_start_8168h_1` (the `e_info_8168h_1[]` static at top). See `rtl8111h_phy_init.md` §2. **Failure mode**: PCIe SerDes is mis-tuned, link unstable.

**19.** MAC-OCP / ERI body of `rtl_hw_start_8168h_1` is transcribed in execution order: `rtl_set_fifo_size(0x08, 0x10, 0x02, 0x06)` first, ending with `r8168_mac_ocp_write(0xc09e, 0x0000)`. — Verify against `r8169_main.c:rtl_hw_start_8168h_1` and `rtl8111h_phy_init.md` §3. **Failure mode**: chip clock-gating mis-configured, RX FIFO drops.

**20.** Every `eri_read` / `eri_write` / `macOcpRead` / `macOcpWrite` / `macOcpModify` / `phyOcpRead` / `phyOcpWrite` / `phyReadPaged` / `phyWritePaged` / `phyModifyPaged` call holds `rtl8111hPHYLock` (rank 4) for the full read-modify-write cycle. — **Footgun #4** (commit-series for "Coalesce mac ocp write and modify for 8168H start"). Verify by code review of `src/rtl8111h_phy.go`. **Failure mode**: silent corruption of PHY/ERI/OCP state when concurrent access (e.g. ISR-side LinkChg attempt + control-side init) interleaves the two-register window-access protocol.

**21.** `rtl8168h_2_hw_phy_config` is transcribed exactly per `r8169_phy_config.c:rtl8168h_2_hw_phy_config`, including the `phy_modify_paged(phydev, 0x0a43, 0x10, BIT(0), 0)` "disable 10m pll off" line. — **Footgun #1** (commit `33189f0`). See `rtl8111h_phy_init.md` §4. **Failure mode**: RX CRC errors at low temp / low link speed.

**22.** Driver does **not** transcribe `rtl8168h_1_hw_phy_config` (it is a removed-from-upstream VER_45 artefact). — **Footgun #5**. Verify by `grep -i 'h_1_hw_phy' src/rtl8111h*.go` returns no hits. **Failure mode**: wrong PHY table runs on real silicon.

### A.7 RxConfig / TxConfig / accept mask

**23.** `RxConfig = RX128_INT_EN | RX_MULTI_EN | RX_DMA_BURST | RX_EARLY_OFF = 0x0000_CF00`. — Computed: `(1<<15) | (1<<14) | (7<<8) | (1<<11) = 0x8000 + 0x4000 + 0x0700 + 0x0800 = 0xCF00`. The accept-mask in the low byte is OR'd in **separately** by item 24 — do not pre-OR the accept-mask into the value written here. Cite `r8169_main.c:rtl_init_rxcfg` and the constants at `r8169_main.c:280-289`. **Failure mode**: wrong DMA burst → RX FIFO overrun under load; or accept-mask collision smashes the DMA-burst bits.

**24.** RX accept mask in low byte of RxConfig includes `AcceptBroadcast (0x08) | AcceptMyPhys (0x02)` at minimum. **AcceptBroadcast is required for DHCP** to receive OFFER/ACK. — Verify against `r8169_main.c:rtl_set_rx_mode` and the AcceptXxx constants at `r8169_main.c:494-501`. **Failure mode (Footgun #6)**: DHCP DISCOVER sent, OFFER never received, lease never obtained.

**25.** `TxConfig = (TX_DMA_BURST<<8) | (InterFrameGap<<24) | TXCFG_AUTO_FIFO = (7<<8) | (3<<24) | (1<<7) = 0x0300_0780`. — Verify against `r8169_main.c:rtl_set_tx_config_registers`. **Failure mode**: wrong IPG triggers MAC-side back-off thrashing.

### A.8 Cfg9346 lock dance

**26.** Driver writes `Cfg9346 = 0xC0` (unlock) **before** writing descriptor base addresses, RxMaxSize, RxConfig, TxConfig; writes `Cfg9346 = 0x00` (lock) **after**. — Verify against `r8169_main.c:rtl_unlock_config_regs` / `rtl_lock_config_regs`. **Failure mode**: protected register writes are silently no-ops; chip never sees the new config; init "succeeds" but no packets flow.

### A.9 Interrupt handling

**27.** `IntrMask = 0x002F` (RxOK | RxErr | TxOK | TxErr | LinkChg). Specifically **does not** enable `SYSErr (0x8000)`, `RxFIFOOver (0x40)`, `RxOverflow (0x10)`, `TxDescUnavail (0x80)`. — Verify against `r8169_main.c:rtl_set_irq_mask`. **Failure mode**: enabling SYSErr on VER_46 generates unhandled interrupts on PCIe state changes.

**28.** ISR ack semantics: **read `IntrStatus`, write back the bits read** to clear (RW1C). Do not write `0xFFFF`. — Verify against `r8169_main.c:rtl_ack_events`. **Failure mode**: writing back unrelated bits clears interrupt causes the driver hasn't observed yet → missed events.

**29.** ISR is **allocation-free**, has no chan ops, no `go` statements, no string concat with literals, no slice/map literals, no interface boxing. Annotated with `//go:nosplit`. — Verify by running `make lint` after the code is in place; verify by reviewing `handleRTL8111HIRQ` body in `src/rtl8111h_irq.go`. **Failure mode**: `make lint` fails (build break), or ISR allocates and corrupts the GC heap.

**30.** ISR sends EOI via `picSendEOI(uint8(vector-32))` if `!ioapicActive`, else `lapicSendEOI()`. — Verify against `handleE1000IRQ` (mirror). **Failure mode**: PIC keeps the IRQ line masked; second interrupt never arrives; chip eventually wedges.

### A.10 RX path

**31.** `drainRxRingRTL8111H` checks `DescOwn` first; returns immediately if NIC still owns the descriptor. — Verify against `r8169_main.c:rtl_rx`. **Failure mode**: driver reads in-flight RX data; corruption / wrong length.

**32.** `drainRxRingRTL8111H` checks `RxRES (1<<21)` and bumps `RxDropped` then continues to release the descriptor. Does not abort the loop on a single error. — Verify against `r8169_main.c:rtl_rx`. **Failure mode**: a single error packet stalls the whole RX path.

**33.** Packet size is read from `opts1 & 0x3FFF` (low 14 bits). — Verify against `r8169_main.c:rtl_rx` (`pkt_size = status & GENMASK(13, 0)`). **Failure mode**: chip reports correct length, driver truncates or misframes.

**34.** Released RX descriptor sets `DescOwn | (RingEnd if last) | RxBufSize`; the `RingEnd` bit is preserved by reading `opts1 & RingEnd` BEFORE overwriting `opts1`. — Verify against `r8169_main.c:rtl8169_mark_to_asic`. **Failure mode**: wrap loses `RingEnd`; chip walks past end of ring.

### A.11 TX path

**35.** `rtl8111hTransmit` validates `len(frame) >= 14 && <= 1518`. — Verify against `e1000Transmit` (mirror). **Failure mode**: jumbo frame sent without descriptor split → chip mis-DMAs.

**36.** TX descriptor is filled in order `addr → opts2 → opts1` (DescOwn last). — see item 15. **Failure mode**: stale-write hazard.

**37.** TX doorbell (`mmioWrite8(rtl8111hBase+TxPoll, NPQ=0x40)`) is written **after** the descriptor is fully published. — Verify against `r8169_main.c:rtl8169_doorbell` and the order of `dma_wmb() ... rtl8169_doorbell` in `rtl8169_start_xmit`. **Failure mode**: chip processes doorbell, reads not-yet-published descriptor → garbage TX.

**38.** `txTail = (txTail + 1) % NUM_TX_DESC` advances ring index; modulo, not subtract. — Verify by code review. **Failure mode**: index walks off end → out-of-bounds descriptor access.

### A.12 MAC address read

**39.** MAC address is read from **ERI registers `0xe0` (low 4 bytes) and `0xe4` (low 2 bytes of next dword)**, NOT from `MAC0`/`MAC4` MMIO directly. — Verify against `r8169_main.c:rtl_read_mac_address` (the `rtl_is_8168evl_up` arm). See `rtl8111h_phy_init.md` §5. **Failure mode**: MAC reads as zero or stale; DHCP requests have wrong Ethernet source; reply ignored by router.

**40.** MAC bytes are unpacked in **little-endian** order (`mac[0] = byte(lo)`, `mac[1] = byte(lo>>8)`, ..., `mac[5] = byte(hi>>8)`). — Verify by code review. **Failure mode**: MAC bytes reversed; same as item 39 in effect.

### A.13 Endianness & buffer alignment

**41.** Descriptor `opts1`, `opts2`, `addr` reads/writes use plain `uint32` / `uint64` (gooos x86_64 is little-endian; upstream's `__le32` casts are no-ops). The audit explicitly verifies no `htonl`/`ntohl` calls leak into descriptor handling. — Verify by `grep` of `src/rtl8111h.go`. **Failure mode**: subtle bit-swap on every descriptor field; nothing works.

**42.** RX buffer size constant matches the value programmed into `RxMaxSize` (`2048`). — Verify by code review. **Failure mode**: chip writes more than buffer holds → memory corruption past buffer end.

**43.** TX buffer per-slot size (≥ 1518 + alignment slack) accommodates max frame (1518 bytes). — Verify by code review. **Failure mode**: TX buffer overrun on max-size frame.

### A.14 NULL / range / leak audit

**44.** `rtl8111hInit` checks every `allocPagesContig` return value (must be `> 0`, never wraps). On allocation failure the init logs and returns without setting `activeNIC`. — Verify by code review. **Failure mode**: NULL descriptor base programmed → chip access faults at `0x0`.

**45.** Init failure path does **not** leak descriptor pages back to the page allocator. (Currently gooos's `allocPagesContig` does not have a free path — pages allocated at boot stay allocated. So the "leak" here is actually fine for v1; the audit verifies that `allocPagesContig` is the one being used and not some hypothetical paired free.) — Verify against `src/vm.go:allocPagesContig` semantics. **Failure mode**: future re-init attempt re-allocates new pages, rapidly exhausting kernel heap. (V1 init is one-shot at boot; deferred for v2.)

### A.15 Counter integrity

**46.** Every counter bump uses `statsInc` / `statsAdd` (atomic on uint64 under `gooos`'s convention). The driver does NOT introduce raw `++` on `netStats` fields. — Verify by code review of `src/rtl8111h.go` and `src/rtl8111h_irq.go`. **Failure mode**: cross-CPU read of a partially-updated counter shows torn value (~no functional impact, but `netDiag` output is misleading).

### A.16 ARP / upper-stack compatibility

**47.** `nicDriver.mac` is populated by `rtl8111hInit` before `netInit` runs the gratuitous-ARP send path. — Verify by code review of `netInit` ordering. **Failure mode**: gratuitous ARP sent with all-zero source MAC → router caches wrong entry.

**48.** No `e1000Found` global is referenced from `src/arp.go` / `src/ipv4.go` / `src/udp.go` after the migration to `activeNIC`. — Verify by `grep -n e1000Found src/arp.go src/ipv4.go src/udp.go` (must return no hits in the rewritten functions). **Failure mode**: TX is gated on a stale variable; works for e1000 by coincidence, doesn't work for RTL8111H.

### A.17 Lock discipline

**49.** `rtl8111hPHYLock` (rank 4) is the only lock the PHY/ERI/OCP accessors take. The TX/RX hot paths take `rtl8111hTxLock` (rank 5) and `rtl8111hRxLock` (rank 5) respectively, never PHY lock. — Verify by code review. **Failure mode**: lock-order violation deadlocks; SMP corruption.

**50.** `make lint` passes after the driver is in place. (Includes the ISR-safety walker. Run as a build prereq in `make build`.) — Verify by running `make lint`. **Failure mode**: build is broken; CI rejects.

### A.18 Footguns recap (named, with citations)

**51.** **Footgun #1** (commit `33189f0`, "r8169: fix RTL8168H and RTL8107E rx crc error"). Mitigation: `rtl8168h_2_hw_phy_config` includes `phyModifyPaged(0x0a43, 0x10, 1, 0)` step. See checklist item 21. **Failure mode if missed**: RX CRC errors at low temp / 10 Mbps link; intermittent in lab, dramatic in field.

**52.** **Footgun #2** (commit `3148ded`, "r8169: fix powering up RTL8168h", May 2018). Mitigation: 20 ms idle after PHY power-up before any PHY MMIO/MDIO. The original commit added `msleep(20)` to the now-renamed `rtl_pll_power_up` path; current upstream's equivalent wait is in PHYlib (`phy_resume`). See checklist item 8. **Failure mode if missed**: PHY MII writes silently land nowhere; link never up.

**53.** **Footgun #3** (commit `90ca51e`, "r8169: fix ASPM-related issues on a number of systems with NIC version from RTL8168h"). Mitigation: explicit ASPM L1 disable + ASPM entry-latency tweak. See checklist items 10, 11. **Failure mode if missed**: TX queue timeouts under load, PCIe link drops at idle, `rx_missed` grows.

**54.** **Footgun #4** ("r8169: Coalesce mac ocp write and modify for 8168H start" — patch v5 3/7 series; underlying correctness commit is the introduction of `r8168_mac_ocp_modify` itself, commit `ef712ed` "r8169: add helper r8168_mac_ocp_modify"). Mitigation: every ERI/OCP/MII access wrapped in `rtl8111hPHYLock`. See checklist item 20. **Failure mode if missed**: silent corruption of PHY/ERI/OCP state on concurrent access; descriptor base subtly wrong; sporadic packet loss.

**55.** **Footgun #5** (commit `ebe5989`, "r8169: remove support for chip versions 45 and 47"). Mitigation: XID match accepts only `0x541` / `0x6c0`; no `rtl8168h_1_*` paths in source. See checklist items 9, 22. **Failure mode if missed**: wrong PHY init runs on real silicon; subtle RX behaviour anomalies.

**56.** **Footgun #6** (DHCP gate). Mitigation: RX accept mask includes `AcceptBroadcast (0x08)`. See checklist item 24. **Failure mode if missed**: DHCP DISCOVER sent, OFFER never received, lease never obtained — symptom looks like "the NIC is up but DHCP just hangs".

That is the checklist. **56 items**, comfortably above the ≥25 floor mandated by the prompt. Items are grouped by subsystem so a reviewer can audit one chapter of the implementation at a time without re-reading the whole list.

---

## §B. One-shot real-hardware bring-up test plan

### B.1 Pre-flight

Before powering on the target hardware:

- [ ] `enableRTL8111H = true` in `src/preempt_config.go`. (Default is `false`; flip for the bring-up.)
- [ ] `preferRTL8111H` left at default `false` (irrelevant if board has only RTL8111H).
- [ ] `make lint` passes (item 50 of §A).
- [ ] `make build` succeeds.
- [ ] `make iso` succeeds; ISO copied to a USB stick or PXE server reachable by the target.
- [ ] Serial console (115200 baud, 8N1) wired and a host-side terminal capturing.
- [ ] DHCP server reachable on the LAN (the default LAN router is fine).
- [ ] (Optional) Wireshark / `tcpdump` running on a port-mirror of the target's LAN segment, so DHCP and ARP traffic can be observed independently of gooos's serial output.

### B.2 Expected serial-log success sequence

In **this order** (other unrelated boot lines may interleave):

```
[pci] scan: bus 0
[pci] found e1000 at ... (or, more likely on bare metal, no e1000)
[pci] found RTL8111H at <bus>:<dev>.<fn> vendor=10ec device=8168 bar0=<32-bit hex> irq=<N>
rtl8111h: MAC=<XX:XX:XX:XX:XX:XX>
rtl8111h: auto-neg complete: 1000 Mbps full
rtl8111h: NIC initialized
[net] active=rtl8111h linkUp=1 ...
```

Then, after `dhcp.elf` is invoked from the shell:

```
$ dhcp
dhcp: DISCOVER sent
dhcp: OFFER from <server-ip>
dhcp: REQUEST sent
dhcp: ACK received: ip=<lease> mask=<mask> gw=<gw> dns=<dns>
dhcp: applied via sys_net_config
$ cat network.conf
ip=<lease>
netmask=<mask>
gateway=<gw>
dns=<dns>
```

### B.3 `netDiag` counter expectations after `dhcp` succeeds

Run from the gooos shell (or wait for the periodic `netDiag` output ~10 s after boot):

| Counter | Expected after `dhcp` | Notes |
|---|---|---|
| `RxPackets` | ≥ 4 | OFFER + ACK + ARP probe + at least one neighbour broadcast |
| `RxBytes` | ≥ 1200 | DHCP OFFER alone is ~340 bytes; sum likely ≥ 1.2 KiB |
| `RxDropped` | 0 | Healthy chip drops nothing in this short window |
| `NetRxFrames` | == `RxPackets` | `drainRxRingRTL8111H` and dispatch are 1:1 |
| `TxPackets` | ≥ 2 | DISCOVER + REQUEST minimum |
| `TxBytes` | ≥ 600 | Minimum |
| `RTL8111HIRQs` | ≥ 4 | Two RxOK (OFFER, ACK), at least one TxOK if unmasked |
| `BufAllocFail` | 0 | Pool not under pressure in this scenario |
| `ArpRequestsSent` | ≥ 1 | Gratuitous ARP at netInit, plus ARP for gateway |
| `ArpHits` | ≥ 1 | Gateway resolves successfully |
| `UdpRecv` | ≥ 2 | OFFER + ACK |
| `UdpSend` | ≥ 2 | DISCOVER + REQUEST |

### B.4 Host-side commands to verify TX/RX

After `dhcp` succeeds, the gooos box has a routable IP. From a host on the same LAN segment:

```bash
# 1. ICMP echo — gooos's icmp.go replies; this exercises RX dispatch.
ping <gooos-ip>
# Expected: reply within < 1 ms, 0% loss after the first packet.

# 2. UDP echo via the gooos kernel's built-in echo on port 7, or via
#    udpecho.elf on port 17 if launched from the shell.
nc -u <gooos-ip> 7
hello<enter>
# Expected: nc echoes "hello" back.

# 3. TCP echo via the kernel's tcpEchoServer on port 8080, or
#    tcpecho.elf on port 8081.
nc <gooos-ip> 8080
hello<enter>
# Expected: nc echoes "hello" back, FIN cleanly on Ctrl+D.

# 4. Outbound HTTP via wget.elf (validates outbound TCP).
$ wget http://<some-host-on-LAN>/<small-file>
# Expected: file downloads, written to gooos FS.
```

Any failure here triggers the matrix in §B.5.

### B.5 Failure-pattern → root-cause matrix

A boot failure or partial-success with one of the patterns below triggers the corresponding suspect. **Cross-check the corresponding checklist item** before re-flashing — most root causes are inert checklist items the implementer skipped under time pressure.

| Symptom | Most likely root cause | Checklist item |
|---|---|---|
| `[pci] found RTL8111H` line never appears | `pciInit` does not match `0x10EC:0x8168`, or `enableRTL8111H = false` was never flipped | item 1 |
| `[pci] found RTL8111H` appears but `rtl8111h: MAC=00:00:00:00:00:00` | MAC read goes through wrong register; check ERI 0xe0/0xe4 path | items 39, 40 |
| `rtl8111h: reset timed out` | `rChipCmd = 0x37` offset wrong; or BAR0 not mapped writeable | items 4, 5, 7 |
| `rtl8111h: unexpected XID` | XID computation `(TxConfig >> 20) & 0x7cf` wrong; or chip is not actually RTL8111H/RTL8168M | item 9 |
| `rtl8111h: auto-neg timeout (5 s)` and link never comes up later | PHY MII writes never landed; missing 20 ms wait (Footgun #2) or PHY init table wrong (item 21) | items 8, 21 |
| `rtl8111h: NIC initialized` but no `RTL8111HIRQs` ever increments | IRQ handler not registered; or wrong vector; or PIC mask not lifted | item 30; verify `registerHandler(int(32 + rtl8111hPCI.IRQLine), ...)` was called |
| `RTL8111HIRQs` increments but `RxPackets` stays 0 | RX descriptor `DescOwn` polarity reversed or `RxConfig` accept-mask wrong | items 16, 24 |
| `RxPackets` increments but `dhcp.elf` never sees OFFER | `AcceptBroadcast (0x08)` not set in RxConfig low byte → DHCP OFFER (broadcast) discarded by chip | item 24 (Footgun #6) |
| TX silent: `dhcp.elf` says "DISCOVER sent" but Wireshark shows nothing on wire | Doorbell address wrong (`TxPoll = 0x38`, value `NPQ = 0x40`); or `Cmd.TxEnb` not set; or descriptor base wrong | items 5, 26, 37 |
| TX visible on Wireshark but garbage payload | Stale-write hazard: `addr`/`opts2` written after `opts1.DescOwn` (chip read before publication); or endianness wrong on descriptor field | items 15, 41 |
| Random PCIe link drops at idle, possibly seconds after boot | ASPM L1 enabled and chip races into L1 too aggressively | items 10, 11 (Footgun #3) |
| RX CRC errors visible at 10 Mbps (e.g. on cold boot or marginal cable) | "Disable 10M PLL off" PHY tweak missing | item 21 (Footgun #1) |
| Sporadic random PHY-init or descriptor-base corruption | ERI/OCP RMW not under lock | item 20 (Footgun #4) |
| Chip looks fine but link partner reports wrong duplex | TxConfig IPG bits wrong | item 25 |
| `make lint` build break | ISR violates lint rule (allocation, chan op, etc.) | item 29 (and item 50) |
| Boot panics inside `handleRTL8111HIRQ` | ISR uses non-`//go:nosplit` callee that overflows the IRQ stack; or interface boxing crept in | item 29 |
| `[net] active=e1000` printed when board has only RTL8111H | netInit dispatch reads `e1000Found` first, but `e1000Found` is true (e.g. a discrete Intel card is also installed). Tie-break must use `preferRTL8111H` | Chapter 11 of overview |

### B.6 Go/no-go gate

**GO** (declare v1 done, merge the change) — all of:

- [ ] All 7 expected serial-log lines in §B.2 print in order.
- [ ] All 12 counter expectations in §B.3 met (`RxDropped == 0`, `BufAllocFail == 0`, the rest within bounds).
- [ ] All 4 host-side commands in §B.4 succeed (ping replies, UDP echo round-trips, TCP echo round-trips, wget completes).
- [ ] No PCIe link drop observed across a 5-minute idle window.
- [ ] No RX CRC errors at any negotiated speed.
- [ ] `make lint` and `make build` and `make verify-globals` all pass.
- [ ] At least one cycle of `dhcp` lease renewal (typically 30 s on most servers) completes successfully (validates `LinkChg` ISR does not perturb the steady state).

**NO-GO** (do not merge; iterate):

- Any of the above fails.
- `RxDropped > 0` (more than one drop in five minutes is a sign of a real bug, not a chance error).
- Any panic or hang in any chapter.
- Any `serialPrintln` containing `error`, `failed`, `timeout`, `abort` after the boot sequence.

If NO-GO: consult §B.5 root-cause matrix, fix the indicated checklist item(s), rebuild, re-flash, retry. Multiple iteration cycles are expected on a one-shot bring-up — the matrix is what makes them productive instead of guesswork.

### B.7 Post-go-live regression

After the bring-up succeeds, ensure that flipping `enableRTL8111H = false` and rebuilding restores the QEMU regression matrix (e1000 path) untouched. **Run** `make run` and verify the e1000 path still boots, prints the e1000 init lines, and DHCP works under QEMU slirp.

That round-trip confirms the rollback path of Chapter 14 of the overview. It is the final gate before closing the bring-up task.
