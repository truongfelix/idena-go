#!/usr/bin/env python3
"""Check an Idena identity list (consensus v13) against the block header that carries it.

From consensus v13, each snapshot block and each block that applies a ceremony's results carries in its header
the CID of an index block listing every identity in the state after that block. This script:

  1. reads that CID from the header of the block at HEIGHT, from one or more nodes (--rpc, repeatable): they must
     all report the same CID;
  2. downloads the index and its pages (from an IPFS gateway with --gateway, otherwise from the first node's
     ipfs_get) and checks that the SHA-256 of each block is the one its CID names;
  3. decodes the rows (format version 2), checks their order and count, and prints a summary; --csv writes them.

Python 3.8+, standard library only. Exit code: 0 the list matches, 1 it does not, 2 it could not be checked.

  ./check_identity_list.py --rpc http://localhost:9009 --key "$(cat datadir/api.key)" 1234000
  ./check_identity_list.py --rpc http://node-a:9009 --rpc http://node-b:9009 --gateway https://ipfs.io 1234000
"""

import argparse
import base64
import csv
import hashlib
import json
import struct
import sys
import urllib.request
from collections import Counter
from decimal import Decimal

VERSION = 2
ADDRESS = 20
AMOUNT = 32
SCORES = 10
ROW_SIZE = ADDRESS + 1 + 2 + 3 * AMOUNT + 1 + ADDRESS + AMOUNT + 2 + 8 + 4 + 4 + 1 + SCORES + 2 + 2  # 205
PAGE_SIZE = 1000
DNA = Decimal(10) ** 18
STATES = {0: "Undefined", 1: "Invite", 2: "Candidate", 3: "Verified", 4: "Suspended", 5: "Killed", 6: "Zombie",
          7: "Newbie", 8: "Human"}
RAW_CODEC, SHA2_256 = 0x55, 0x12


class Mismatch(Exception):
    pass


def varint(data, pos):
    value, shift = 0, 0
    while True:
        b = data[pos]
        pos += 1
        value |= (b & 0x7F) << shift
        if not b & 0x80:
            return value, pos
        shift += 7


def cid_bytes(cid):
    """Binary form of a CIDv1 written in base32 ('b...')."""
    if not cid.startswith("b"):
        raise ValueError("only base32 CIDv1 strings are expected, got %r" % cid)
    body = cid[1:].upper()
    return base64.b32decode(body + "=" * (-len(body) % 8))


def cid_string(raw):
    return "b" + base64.b32encode(raw).decode().lower().rstrip("=")


def cid_digest(raw):
    """The SHA-256 a raw CIDv1 names."""
    version, pos = varint(raw, 0)
    codec, pos = varint(raw, pos)
    code, pos = varint(raw, pos)
    length, pos = varint(raw, pos)
    if (version, codec, code, length) != (1, RAW_CODEC, SHA2_256, 32) or len(raw) != pos + 32:
        raise Mismatch("CID %s is not a CIDv1 raw SHA2-256" % cid_string(raw))
    return raw[pos:]


def rpc(url, key, method, *params):
    body = {"jsonrpc": "2.0", "id": 1, "method": method, "params": list(params)}
    if key:
        body["key"] = key
    req = urllib.request.Request(url, json.dumps(body).encode(), {"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=60) as resp:
        answer = json.load(resp)
    if answer.get("error"):
        raise RuntimeError("%s %s: %s" % (url, method, answer["error"].get("message", answer["error"])))
    return answer.get("result")


def fetch(raw_cid, args):
    cid = cid_string(raw_cid)
    if args.gateway:
        url = args.gateway.rstrip("/") + "/ipfs/" + cid + "?format=raw"
        req = urllib.request.Request(url, headers={"Accept": "application/vnd.ipld.raw"})
        with urllib.request.urlopen(req, timeout=120) as resp:
            data = resp.read()
    else:
        data = bytes.fromhex(rpc(args.rpc[0], args.key, "ipfs_get", cid)[2:])
    if hashlib.sha256(data).digest() != cid_digest(raw_cid):
        raise Mismatch("block %s: its SHA-256 is not the one its CID names" % cid)
    return data


def decode_index(data):
    if len(data) < 17 or data[0] != VERSION:
        raise Mismatch("index: unknown version or too short")
    height, count, pages = struct.unpack(">QII", data[1:17])
    cids, pos = [], 17
    for _ in range(pages):
        n = data[pos]
        cids.append(data[pos + 1:pos + 1 + n])
        pos += 1 + n
    if pos != len(data):
        raise Mismatch("index: %d bytes after the last page CID" % (len(data) - pos))
    if pages != (count + PAGE_SIZE - 1) // PAGE_SIZE:
        raise Mismatch("index: %d pages for %d identities" % (pages, count))
    return height, count, cids


def amount(b):
    return Decimal(int.from_bytes(b, "big")) / DNA


def decode_page(data):
    if not data or data[0] != VERSION or (len(data) - 1) % ROW_SIZE:
        raise Mismatch("page: unknown version or not whole rows")
    rows = []
    for off in range(1, len(data), ROW_SIZE):
        r = data[off:off + ROW_SIZE]
        p = 0

        def take(n):
            nonlocal p
            p += n
            return r[p - n:p]

        address = take(ADDRESS)
        state = take(1)[0]
        birthday, = struct.unpack(">H", take(2))
        stake, locked, replenished = amount(take(AMOUNT)), amount(take(AMOUNT)), amount(take(AMOUNT))
        has_delegatee = take(1)[0] == 1
        delegatee = take(ADDRESS)
        penalty = amount(take(AMOUNT))
        penalty_seconds, = struct.unpack(">H", take(2))
        penalty_timestamp, = struct.unpack(">q", take(8))
        qualified_flips, short_points = struct.unpack(">II", take(8))
        n = take(1)[0]
        scores = take(SCORES)[:min(n, SCORES)]
        fq_flips, fq_points = struct.unpack(">HH", take(4))
        rows.append({
            "address": "0x" + address.hex(), "state": STATES.get(state, "Unknown(%d)" % state),
            "birthday": birthday, "stake": stake, "lockedStake": locked, "replenishedStake": replenished,
            "delegatee": "0x" + delegatee.hex() if has_delegatee else "", "penalty": penalty,
            "penaltySeconds": penalty_seconds, "penaltyTimestamp": penalty_timestamp,
            "qualifiedFlips": qualified_flips, "shortFlipPoints": Decimal(short_points) / 2,
            "scores": scores.hex(),
            # v13 flip quality window: rewards scale by points / flips (whole while flips is 0)
            "flipQualityFlips": fq_flips, "flipQualityPoints": Decimal(fq_points) / 2,
        })
    return rows


def check(args):
    cids = {}
    for url in args.rpc:
        block = rpc(url, args.key, "bcn_blockAt", args.height)
        if not block:
            raise RuntimeError("%s has no block at height %d" % (url, args.height))
        cids[url] = block.get("identityListCid")
    if None in cids.values():
        raise Mismatch("block %d carries no identity list (on %s)" %
                       (args.height, ", ".join(u for u, c in cids.items() if c is None)))
    if len(set(cids.values())) != 1:
        raise Mismatch("the nodes report different CIDs: %s" % cids)
    index_cid = cid_bytes(next(iter(cids.values())))
    height, count, page_cids = decode_index(fetch(index_cid, args))
    if height != args.height:
        raise Mismatch("index is for block %d, the header is block %d" % (height, args.height))
    rows = []
    for i, page_cid in enumerate(page_cids):
        page = decode_page(fetch(page_cid, args))
        if i < len(page_cids) - 1 and len(page) != PAGE_SIZE:
            raise Mismatch("page %d holds %d rows, not %d" % (i, len(page), PAGE_SIZE))
        rows += page
    if len(rows) != count:
        raise Mismatch("%d rows, the index says %d" % (len(rows), count))
    addresses = [r["address"] for r in rows]
    if any(a >= b for a, b in zip(addresses, addresses[1:])):
        raise Mismatch("rows are not sorted by address, or an address is listed twice")
    return cid_string(index_cid), rows


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("height", type=int, help="height of the block that carries the list")
    parser.add_argument("--rpc", action="append", required=True, help="node RPC URL (repeat to compare nodes)")
    parser.add_argument("--key", default="", help="the nodes' RPC key")
    parser.add_argument("--gateway", help="IPFS gateway to read the blocks from, instead of the first node")
    parser.add_argument("--csv", help="write the identities to this CSV file")
    args = parser.parse_args()
    try:
        cid, rows = check(args)
    except Mismatch as e:
        print("MISMATCH:", e)
        return 1
    except Exception as e:  # network, RPC, malformed input
        print("NOT CHECKED:", e)
        return 2
    by_state = Counter(r["state"] for r in rows)
    print("OK: block %d, list %s, %d identities, all blocks match their CIDs" % (args.height, cid, len(rows)))
    print("   " + ", ".join("%s %d" % kv for kv in sorted(by_state.items())))
    print("   total stake %s iDNA" % sum((r["stake"] for r in rows), Decimal(0)))
    if args.csv:
        with open(args.csv, "w", newline="") as f:
            w = csv.DictWriter(f, fieldnames=list(rows[0].keys()) if rows else ["address"])
            w.writeheader()
            w.writerows(rows)
        print("   rows written to", args.csv)
    return 0


if __name__ == "__main__":
    sys.exit(main())
