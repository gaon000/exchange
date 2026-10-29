"""Independent EXSNP001 envelope around the separately authored state fixture."""
import hashlib
import json
from pathlib import Path
import struct

here = Path(__file__).parent
internal = here.parents[1]
config = json.loads((internal / 'wal/testdata/config.json').read_bytes())
config['accounts'] = [dict(accountId='a', baseLots='0', quoteAtoms='1000'),
                      dict(accountId='b', baseLots='5', quoteAtoms='0')]
canonical = lambda value: json.dumps(value, separators=(',', ':')).encode('ascii')
config_bytes = canonical(config)
state = json.loads((internal / 'engine/testdata/checkpoint-v1.json').read_bytes())
document = dict(runId='11111111-1111-4111-8111-111111111111',
                configHash=hashlib.sha256(config_bytes).hexdigest(),
                rulesVersion=1, responseVersion=1, snapshotSeq='3', state=state)
payload = canonical(document)
header = b'EXSNP001' + struct.pack('<IQ', 1, len(payload))
(here / 'config.json').write_bytes(config_bytes)
(here / 'snapshot.bin').write_bytes(header + payload + hashlib.sha256(header + payload).digest())
