package tunnel

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"
	"time"
)

func (t *Tunnel) send_sack () (err error) {
	t.logger.Warning("sack send")
	buf := bytes.NewBuffer([]byte{})
	for i, p := range t.recvbuf {
		if i > 0x7f { break }
		binary.Write(buf, binary.BigEndian, p.seq)
	}
	return t.send(SACK, buf.Bytes())
}

func (t *Tunnel) send (flag uint8, content []byte) (err error) {
	if t.status != EST && flag == 0{
		return fmt.Errorf("can't send data, %s, pkt: %s", t, DumpFlag(flag))
	}

	if t.recvack != t.recvseq { flag |= ACK }
	retrans := (flag & SACK) == 0 && (flag != ACK || len(content) != 0)
	err = t.send_packet(NewPacket(t, flag, content), retrans)
	if err != nil { return }

	switch {
	case (flag & SACK) != 0:
	case len(content) > 0: t.sendseq += int32(len(content))
	case flag != ACK: t.sendseq += 1
	}
	t.check_windows_block()

	t.recvack = t.recvseq
	if t.delayack != nil { t.delayack = nil }
	return
}

func (t *Tunnel) send_packet(pkt *Packet, retrans bool) (err error) {
	t.logger.Debug("send", pkt)

	if DROPFLAG && rand.Intn(100) >= 85 {
		t.logger.Debug("drop packet")
	}else{
		t.c_send <- &SendBlock{t.remote, pkt}
	}
	if !retrans { return }

	pkt.t = time.Now()
	t.sendbuf.Push(pkt)

	if t.retrans == nil {
		// WARN: is this right?
		d := time.Duration(t.rtt + t.rttvar << 2)
		t.retrans = time.After(d * time.Microsecond)
	}
	return
}

func (t *Tunnel) resend (stopid int32, stop bool) (err error) {
	for _, p := range t.sendbuf {
		err = t.send_packet(p, false)
		if err != nil { return }
		if stop && (p.seq - stopid) >= 0 { return }
	}
	return
}

func (t *Tunnel) on_retrans () (err error) {
	t.retrans_count += 1
	if t.retrans_count > MAXRESEND {
		t.logger.Warning("send packet more then maxretrans times")
		t.send(RST, []byte{})
		t.c_event <- EV_END
		return
	}

	err = t.resend(0, false)
	if err != nil { return }

	inairlen := int32(0)
	if len(t.sendbuf) > 0 { inairlen = t.sendseq - t.sendbuf[0].seq }
	t.ssthresh = max32(inairlen/2, 2*SMSS)

	d := (t.rtt + t.rttvar << 2) * (1 << t.retrans_count)
	t.retrans = time.After(time.Duration(d) * time.Microsecond)
	return
}

func (t *Tunnel) check_windows_block () {
	inairlen := int32(0)
	if len(t.sendbuf) > 0 { inairlen = t.sendseq - t.sendbuf[0].seq }
	switch {
	case (inairlen >= t.sendwnd) || (inairlen >= t.cwnd):
		t.logger.Warning("blocking,", inairlen, t.sendwnd, t.cwnd, t.ssthresh)
		t.c_write = nil
	case t.status == EST && t.c_write == nil:
		t.logger.Warning("restart,", inairlen, t.sendwnd, t.cwnd, t.ssthresh)
		t.c_write = t.c_wrbak // when inairlen < pkt.window
	}
}