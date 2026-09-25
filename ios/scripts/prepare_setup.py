"""Train the personal model; write private USB provisioning outside the repository."""
import argparse
import json
import os
import sqlite3
from collections import defaultdict
from pathlib import Path

import numpy as np
from sklearn.linear_model import LogisticRegression
from sklearn.preprocessing import StandardScaler

parser = argparse.ArgumentParser()
parser.add_argument('private_directory', type=Path)
args = parser.parse_args()
root = args.private_directory
blocks = defaultdict(list)
labels = {}
for line in (root / 'calibration-20260913T061345Z.jsonl').read_text().splitlines():
    row = json.loads(line)
    if 'samples' not in row:
        continue
    blocks[row['block']].extend(row['samples'])
    labels[row['block']] = row['label']

def features(a):
    return np.r_[a.mean(axis=0), a.std(axis=0), np.linalg.norm(a, axis=1).std(),
                 np.linalg.norm(np.diff(a, axis=0), axis=1).mean()]

xs, ys = [], []
for block, samples in blocks.items():
    a = np.asarray(samples, dtype=float) / 1000
    for start in range(0, len(a) - 48, 25):
        xs.append(features(a[start:start + 49]))
        ys.append(int(labels[block] in ('hair', 'cheek')))
scaler = StandardScaler().fit(xs)
model = LogisticRegression(C=0.5, class_weight='balanced', max_iter=1000).fit(scaler.transform(xs), ys)
model_data = dict(mean=scaler.mean_.tolist(), scale=scaler.scale_.tolist(),
                  weights=model.coef_[0].tolist(), intercept=float(model.intercept_[0]))
with sqlite3.connect(f'file:{root / "oura-app.sqlite"}?mode=ro', uri=True) as db:
    peripheral = db.execute('SELECT ios_peripheral_uuid FROM ringconfiguration WHERE in_active_use=1 AND deleted_at IS NULL').fetchone()[0]
setup = dict(keyHex=(root / 'ring-auth-key.hex').read_text().strip(), peripheralID=peripheral, model=model_data)
path = root / 'faceguard-setup.json'
fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, 'w') as output:
    json.dump(setup, output)
# Golden parity fixture deliberately excludes the key and peripheral identifier.
fixture = dict(model=model_data, samples=np.asarray(next(iter(blocks.values())))[:49].tolist(),
               probability=float(model.predict_proba(scaler.transform([xs[0]]))[0, 1]))
(root / 'model-parity.json').write_text(json.dumps(fixture))
print(f'Trained {len(xs)} windows from {len(blocks)} blocks; private setup prepared.')
print('Calibration labels:', {str(k): labels[k] for k in blocks})
print('Training accuracy (not independent validation):', round(float(model.score(scaler.transform(xs), ys)), 3))
