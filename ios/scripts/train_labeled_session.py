"""Fit a candidate model from reviewed clips; never treat unreviewed motion as negative."""
import argparse
import json
from collections import defaultdict
from pathlib import Path
import numpy as np
from sklearn.linear_model import LogisticRegression
from sklearn.preprocessing import StandardScaler

p = argparse.ArgumentParser()
p.add_argument('private_directory', type=Path)
p.add_argument('session_directory', type=Path)
a = p.parse_args()
root = a.private_directory
session = json.loads((a.session_directory / 'session.json').read_text())

def features(x):
    return np.r_[x.mean(0), x.std(0), np.linalg.norm(x, axis=1).std(), np.linalg.norm(np.diff(x, axis=0), axis=1).mean()]

def old_score(x):
    m = session['model']
    z = ((x - m['mean']) / m['scale']) @ np.asarray(m['weights']) + m['intercept']
    return 1 / (1 + np.exp(-np.clip(z, -50, 50)))

# Merge overlapping same-label annotations; purge conflicting overlaps rather than guess.
clips = []
for c in sorted(session['candidates'], key=lambda c: c['from']):
    if c['label'] == 'unreviewed': continue
    label = int(c['label'] == 'touch')
    start, end = c['from'] + 978307200, c['through'] + 978307200
    if clips and start <= clips[-1][1]:
        if label != clips[-1][2]: raise ValueError('Conflicting overlapping labels require review')
        clips[-1][1] = max(end, clips[-1][1])
    else: clips.append([start, end, label])

times, motion = [], []
for line in (a.session_directory / 'motion.jsonl').read_text().splitlines():
    r = json.loads(line)
    for i, sample in enumerate(r['samples']):
        times.append(r['time'] - (len(r['samples']) - 1 - i) / r['rate'])
        motion.append(sample)
times, motion = np.array(times), np.array(motion)
events = []
for start, end, label in clips:
    indices = np.flatnonzero((times >= start) & (times <= end))
    windows = []
    for offset in range(0, len(indices) - 48, 25):
        ix = indices[offset:offset + 49]
        if np.max(np.diff(times[ix])) > 0.5: continue
        windows.append(features(motion[ix]))
    if not windows: raise ValueError('Reviewed clip lacks a complete motion window')
    x = np.asarray(windows)
    # A Touch label means a touch somewhere in the clip, not throughout all five seconds.
    # Retain the two strongest original-detector windows as weak positive training examples.
    selected = x[np.argsort(old_score(x))[-min(2, len(x)):]] if label else x
    events.append((x, selected, label))

blocks = defaultdict(list)
labels = {}
for line in (root / 'calibration-20260913T061345Z.jsonl').read_text().splitlines():
    r = json.loads(line)
    if 'samples' in r:
        blocks[r['block']].extend(r['samples']); labels[r['block']] = int(r['label'] in ('hair', 'cheek'))
base = []
for block, samples in blocks.items():
    x = np.asarray(samples) / 1000
    f = np.array([features(x[i:i+49]) for i in range(0, len(x)-48, 25)])
    base.append((f, labels[block]))

def fit(exclude=None):
    groups = base + [(selected, label) for i, (_, selected, label) in enumerate(events) if i != exclude]
    x = np.vstack([f for f, _ in groups]); y = np.concatenate([np.full(len(f), label) for f, label in groups])
    w = np.concatenate([np.full(len(f), 1 / len(f)) for f, _ in groups])
    # Equal event weight, then equal total weight for the two human-label classes.
    for label in [0, 1]: w[y == label] *= len(groups) / (2 * w[y == label].sum())
    scaler = StandardScaler().fit(x, sample_weight=w)
    model = LogisticRegression(C=2, max_iter=3000).fit(scaler.transform(x), y, sample_weight=w)
    return scaler, model

def clip_score(probabilities):
    # At least two consecutive positive windows are required by the app.
    if len(probabilities) < 2: return float(probabilities[0])
    return float(np.max(np.minimum(probabilities[:-1], probabilities[1:])))

threshold = session['threshold']
results = []
for i, (x, _, label) in enumerate(events):
    scaler, model = fit(exclude=i)
    results.append(dict(label=label, old=clip_score(old_score(x)), held_out=clip_score(model.predict_proba(scaler.transform(x))[:,1])))
scaler, model = fit()
model_data = dict(mean=scaler.mean_.tolist(), scale=scaler.scale_.tolist(), weights=model.coef_[0].tolist(), intercept=float(model.intercept_[0]))
for r, (x, _, _) in zip(results, events): r['fitted'] = clip_score(model.predict_proba(scaler.transform(x))[:,1])
def summary(field):
    return {name: sum(r['label'] == label and r[field] >= threshold for r in results) for name, label in [('touch_clips_detected',1), ('negative_clips_flagged',0)]}
report = dict(session=session['id'], threshold=threshold, labels=len(session['candidates']), merged_clips=len(events),
              positive_clips=sum(r['label'] for r in results), negative_clips=sum(1-r['label'] for r in results),
              original=summary('old'), leave_one_clip_out=summary('held_out'), training_fit=summary('fitted'), clips=results,
              limitation='Small selected sample; exploratory leave-one-clip-out checks are not independent prospective validation. Positive intervals use weak labels.')
(root / 'labeled-session-model.json').write_text(json.dumps(model_data))
(root / 'labeled-session-report.json').write_text(json.dumps(report, indent=2))
fixture = dict(model=model_data, samples=(motion[:49] * 1000).tolist(), probability=float(model.predict_proba(scaler.transform([features(motion[:49])]))[0,1]))
(root / 'labeled-model-parity.json').write_text(json.dumps(fixture))
print(json.dumps({k:v for k,v in report.items() if k != 'clips'}, indent=2))
