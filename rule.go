// Copyright (C) 2014 Ian Bishop
//
// This program is free software; you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation; either version 2 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License along
// with this program; if not, write to the Free Software Foundation, Inc.,
// 51 Franklin Street, Fifth Floor, Boston, MA 02110-1301 USA.

package nft

import (
	//	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"
)

/*
#cgo linux LDFLAGS: -lnftnl -lmnl

#include <stdlib.h>
#include <netinet/in.h>

#include <linux/ip.h>
#include <linux/netfilter.h>
#include <linux/netfilter/nf_tables.h>

#include <libmnl/libmnl.h>
#include <libnftnl/rule.h>
#include <libnftnl/expr.h>

extern int go_rule_callback(const struct nlmsghdr *nlh, void *data);
mnl_cb_t get_go_rule_cb() {
        return (mnl_cb_t)go_rule_callback;
}
*/
import "C"

// json wrapper
type jsonRule struct {
	Rule Rule `json:"rule"`
}

type Rule struct {
	Family      string `json:"family"`
	Table       string `json:"table"`
	Chain       string `json:"chain"`
	Handle      uint64 `json:"handle"`
	Position    uint64 `json:"position,omitempty"`
	CompatFlags uint32 `json:"compat_flags,omitempty"`
	CompatProto uint32 `json:"compat_proto,omitempty"`
	Expr        []Expr `json:"expr,omitempty"`

	// Used to store C rule ptr
	rule *C.struct_nft_rule
}

type Expr struct {
	Type   string `json:"type,omitempty"`
	Dreg   int    `json:"dreg,omitempty"`
	Offset int    `json:"offset,omitempty"`
	Len    int    `json:"len,omitempty"`
	Base   string `json:"base,omitempty"`

	Sreg    int    `json:"sreg,omitempty"`
	Op      string `json:"op,omitempty"`
	DataReg struct {
		Type    string `json:"type,omitempty"`
		Len     int    `json:"len,omitempty"`
		Data0   string `json:"data0,omitempty"`
		Verdict string `json:"verdict,omitempty"`
	} `json:"data_reg,omitempty"`
}

type ruleChan chan *Rule

func (r *Rule) String() string {
	return fmt.Sprintf("%s %s %s %d %d", r.Family, r.Table, r.Chain, r.Handle, r.Position)
}

// Get all rules for given chain
func GetRules(chain string) ([]*Rule, error) {
	return getRule(chain, "ip")
}

func GetRules6(chain string) ([]*Rule, error) {
	return getRule(chain, "ip6")
}

func getRule(name, family string) (rules []*Rule, err error) {
	var f int
	var ok bool
	if f, ok = familyMap[family]; !ok {
		err = fmt.Errorf("unrecognised family %s", family)
		return
	}
	seq := time.Time{}.Unix()

	// The following const was not recognised from libmnl.h
	// #define MNL_SOCKET_BUFFER_SIZE (getpagesize() < 8192L ? getpagesize() : 8192L)
	var bufsize int
	if C.getpagesize() < 8192 {
		bufsize = int(C.getpagesize())
	} else {
		bufsize = 8192
	}
	buf := make([]byte, bufsize)
	nlh := C.nft_rule_nlmsg_build_hdr(
		(*C.char)(unsafe.Pointer(&buf[0])),
		C.uint16_t(C.enum_nf_tables_msg_types(C.NFT_MSG_GETRULE)),
		(C.uint16_t)(f),
		C.NLM_F_DUMP,
		C.uint32_t(seq))
	if nlh == nil {
		err = fmt.Errorf("nft_rule_nlmsg_build_hdr")
		return
	}

	nl := C.mnl_socket_open(C.NETLINK_NETFILTER)
	if nl == nil {
		err = fmt.Errorf("mnl_socket_open")
		return
	}

	if C.mnl_socket_bind(nl, 0, C.MNL_SOCKET_AUTOPID) < 0 {
		err = fmt.Errorf("mnl_socket_bind")
		return
	}

	portid := C.mnl_socket_get_portid(nl)

	if C.mnl_socket_sendto(nl, unsafe.Pointer(nlh), (C.size_t)(nlh.nlmsg_len)) < 0 {
		err = fmt.Errorf("mnl_socket_send")
		return
	}

	var cerr error
	var n C.int
	var sn C.ssize_t
	sn = C.mnl_socket_recvfrom(nl, unsafe.Pointer(&buf[0]), (C.size_t)(len(buf)))
	rulech := make(ruleChan)

	readyCh := make(chan bool, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		readyCh <- true
		for {
			select {
			case t, ok := <-rulech:
				if ok {
					rules = append(rules, t)
				} else {
					return
				}
			}
		}
	}()
	<-readyCh

	for sn > 0 {
		n, cerr = C.mnl_cb_run(
			unsafe.Pointer(&buf[0]),
			C.size_t(sn),
			C.uint(seq), portid,
			C.mnl_cb_t(C.get_go_rule_cb()),
			unsafe.Pointer(&rulech))

		sn = C.ssize_t(n)
		if sn <= 0 {
			break
		}
		sn, cerr = C.mnl_socket_recvfrom(nl, unsafe.Pointer(&buf[0]), C.size_t(len(buf)))
	}

	close(rulech)
	wg.Wait()

	if sn == -1 {
		err = fmt.Errorf("mnl_cb_run: err %s", cerr)
		return
	}
	C.mnl_socket_close(nl)

	return
}

func (r *Rule) Add() (err error) {

	err = r.addSetup("tcp", 22)
	if err != nil {
		return
	}

	return
}

func (r *Rule) addSetup(protoStr string, port int) (err error) {

	var proto int
	var ok bool
	if proto, ok = protoMap[protoStr]; !ok {
		err = fmt.Errorf("unrecognised protocol %s", protoStr)
		return
	}
	var cerr error

	r.rule, cerr = C.nft_rule_alloc()
	defer C.free(unsafe.Pointer(r.rule))

	if r == nil {
		err = fmt.Errorf("rule alloc failure: %s", cerr)
		return
	}

	ctable := C.CString(r.Table)
	defer C.free(unsafe.Pointer(ctable))
	C.nft_rule_attr_set(
		r.rule,
		C.NFT_RULE_ATTR_TABLE,
		unsafe.Pointer(ctable),
	)
	cchain := C.CString(r.Chain)
	defer C.free(unsafe.Pointer(cchain))
	C.nft_rule_attr_set(
		r.rule,
		C.NFT_RULE_ATTR_CHAIN,
		unsafe.Pointer(cchain),
	)

	if f, ok := familyMap[r.Family]; ok {
		C.nft_rule_attr_set_u32(
			r.rule,
			C.NFT_RULE_ATTR_FAMILY,
			(C.uint32_t)(f),
		)
	} else {
		err = fmt.Errorf("unrecognised family %s", r.Family)
		return
	}
	if r.Handle > 0 {
		C.nft_rule_attr_set_u64(
			r.rule,
			C.NFT_RULE_ATTR_POSITION,
			(C.uint64_t)(r.Handle),
		)
	}

	var iphdr C.struct_iphdr

	err = r.addPayload(
		int(C.NFT_PAYLOAD_NETWORK_HEADER),
		int(C.NFT_REG_1),
		unsafe.Offsetof(iphdr.protocol),
		8,
	)
	if err != nil {
		return
	}

	err = r.addCmp(
		int(C.NFT_REG_1),
		int(C.NFT_CMP_EQ),
		proto,
		8,
	)
	if err != nil {
		return
	}

	//dport := C.htons(port)
	//add_payload(r, NFT_PAYLOAD_TRANSPORT_HEADER, NFT_REG_1,
	//offsetof(struct tcphdr, dest), sizeof(uint16_t));
	//add_cmp(r, NFT_REG_1, NFT_CMP_EQ, &dport, sizeof(uint16_t));

	//r.addCounter()

	return
}

func (r *Rule) addCmp(sreg, op int, data int, length uintptr) (err error) {

	var cerr error

	ccmp := C.CString("cmp")
	defer C.free(unsafe.Pointer(ccmp))
	// free'd by libnftnl
	e, cerr := C.nft_rule_expr_alloc(ccmp)
	if e == nil {
		err = fmt.Errorf("rule expr alloc failure: %s", cerr)
		return
	}

	C.nft_rule_expr_set_u32(
		e,
		C.NFT_EXPR_CMP_SREG,
		(C.uint32_t)(sreg),
	)
	C.nft_rule_expr_set_u32(
		e,
		C.NFT_EXPR_CMP_OP,
		(C.uint32_t)(op),
	)

	C.nft_rule_expr_set(
		e,
		C.NFT_EXPR_CMP_DATA,
		unsafe.Pointer(&data),
		(C.uint32_t)(length),
	)

	C.nft_rule_add_expr(r.rule, e)

	return
}

func (r *Rule) addPayload(base, dreg int, offset, length uintptr) (err error) {

	var cerr error

	cpayload := C.CString("payload")
	defer C.free(unsafe.Pointer(cpayload))
	// free'd by libnftnl
	e, cerr := C.nft_rule_expr_alloc(cpayload)
	if e == nil {
		err = fmt.Errorf("rule expr alloc failure: %s", cerr)
		return
	}

	C.nft_rule_expr_set_u32(
		e,
		C.NFT_EXPR_PAYLOAD_BASE,
		(C.uint32_t)(base),
	)
	C.nft_rule_expr_set_u32(
		e,
		C.NFT_EXPR_PAYLOAD_DREG,
		(C.uint32_t)(dreg),
	)
	C.nft_rule_expr_set_u32(
		e,
		C.NFT_EXPR_PAYLOAD_OFFSET,
		(C.uint32_t)(offset),
	)
	C.nft_rule_expr_set_u32(
		e,
		C.NFT_EXPR_PAYLOAD_LEN,
		(C.uint32_t)(length),
	)

	C.nft_rule_add_expr(r.rule, e)

	return

}