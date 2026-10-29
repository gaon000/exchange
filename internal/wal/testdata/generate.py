"""Deliberate v1 fixture regeneration: independent LE packing and bitwise CRC32C.

Run from this directory with Python 3; no exchange codec is imported.
Changing these bytes requires reviewing the versioned storage contract.
"""
import hashlib
import json
import struct
from pathlib import Path

HERE = Path(__file__).parent

def wire(value):
    return json.dumps(value, separators=(',', ':')).encode('ascii')

def crc32c(data):
    crc = 0xffffffff
    for byte in data:
        crc ^= byte
        for _ in range(8):
            crc = (crc >> 1) ^ (0x82f63b78 if crc & 1 else 0)
    return crc ^ 0xffffffff

def envelope(magic, payload):
    prefix = magic + struct.pack('<IQ', 1, len(payload)) + payload
    return prefix + hashlib.sha256(prefix).digest()

config = dict(schemaVersion=1, rulesVersion=1, responseVersion=1,
              symbol='BTC-USDT', baseLotDecimals=4, quoteAtomDecimals=6,
              maxPriceTicks='100000000', maxQuantityLots='1000000',
              maxNotionalAtoms='100000000000000', maxBaseSupplyLots='1000000000000',
              maxQuoteSupplyAtoms='1000000000000000000', maxCommands='1000000',
              accounts=[dict(accountId='alice', baseLots='10', quoteAtoms='10000'),
                        dict(accountId='bob', baseLots='20', quoteAtoms='20000')])
run_id = '00000000-0000-4000-8000-000000000001'
config_hash = hashlib.sha256(wire(config)).hexdigest()
genesis = wire(dict(runId=run_id, configHash=config_hash, config=config))
meta = wire(dict(runId=run_id, configHash=config_hash, rulesVersion=1, responseVersion=1))
record = wire(dict(commandSeq='1', kind='PLACE_ORDER', accountId='alice',
                   payload=dict(clientOrderId='buy-1', symbol='BTC-USDT', side='BUY',
                                type='LIMIT', timeInForce='GTC', priceTicks='100', quantityLots='2')))
cancel = wire(dict(commandSeq='2', kind='CANCEL_ORDER', accountId='alice',
                   payload=dict(cancelRequestId='cancel-1', clientOrderId='buy-1')))
payload = struct.pack('<I', len(record)) + record
header = b'EXGRP001' + struct.pack('<IIQII', 36, len(payload), 1, 1, 0)
header += struct.pack('<I', crc32c(header))
prefix = header + payload + b'EXEND001' + struct.pack('<I', 36 + len(payload) + 16)
group = prefix + struct.pack('<I', crc32c(prefix))
for name, data in [('config.json', wire(config)), ('record.json', record), ('cancel.json', cancel),
                   ('genesis.bin', envelope(b'EXGEN001', genesis)),
                   ('commands.wal', envelope(b'EXWAL001', meta) + group)]:
    (HERE / name).write_bytes(data)
