"""Train from reviewed sessions, with entire recording sessions held out for comparison."""
import argparse
import json
from pathlib import Path
import numpy as np
from sklearn.linear_model import LogisticRegression
from sklearn.preprocessing import StandardScaler


def features(x):
    return np.r_[x.mean(0), x.std(0), np.linalg.norm(x, axis=1).std(), np.linalg.norm(np.diff(x, axis=0), axis=1).mean()]


def probability(x, model):
    score = ((x - model['mean']) / model['scale']) @ np.asarray(model['weights']) + model['intercept']
    return 1 / (1 + np.exp(-np.clip(score, -50, 50)))


def clip_score(p):
    return float(np.max(np.minimum(p[:-1], p[1:]))) if len(p) >= 2 else 0.0


def extract(directory, baseline):
    events = []
    conflicts = 0
    sample_fixture = None
    for path in sorted(directory.rglob('session.json')):
        session = json.loads(path.read_text())
        clips = []
        for c in sorted(session['candidates'], key=lambda c: c['from']):
            if c['label'] not in ('touch', 'notTouch'):
                continue
            start, end = c['from'] + 978307200, c['through'] + 978307200
            label = int(c['label'] == 'touch')
            if clips and start <= clips[-1][1]:
                if clips[-1][2] != label:
                    clips[-1][2] = -1
                clips[-1][1] = max(end, clips[-1][1])
            else:
                clips.append([start, end, label])
        samples, times = [], []
        for line in path.with_name('motion.jsonl').read_text().splitlines():
            frame = json.loads(line)
            if frame['rate'] not in (49, 50):
                continue
            for i, sample in enumerate(frame['samples']):
                samples.append(sample)
                times.append(frame['time'] - (len(frame['samples']) - 1 - i) / frame['rate'])
        samples, times = np.asarray(samples), np.asarray(times)
        for start, end, label in clips:
            if label == -1:
                conflicts += 1
                continue
            indices = np.flatnonzero((times >= start) & (times <= end))
            windows = []
            for offset in range(0, len(indices) - 48, 25):
                ix = indices[offset:offset + 49]
                if np.max(np.diff(times[ix])) > 0.5:
                    continue
                windows.append(features(samples[ix]))
                if sample_fixture is None:
                    sample_fixture = samples[ix]
            if len(windows) < 2:
                continue
            x = np.asarray(windows)
            # Positive clips contain a touch somewhere, not five seconds of touching.
            # Keep the strongest adjacent pair, matching the deployed debounce.
            if label:
                p = probability(x, baseline)
                best = int(np.argmax(np.minimum(p[:-1], p[1:])))
                train = x[best:best + 2]
            else:
                train = x
            events.append(dict(session=session['id'], label=label, x=x, train=train))
    return events, conflicts, sample_fixture


def fit(events):
    x = np.vstack([e['train'] for e in events])
    y = np.concatenate([np.full(len(e['train']), e['label']) for e in events])
    w = np.concatenate([np.full(len(e['train']), 1 / len(e['train'])) for e in events])
    for label in (0, 1):
        if not np.any(y == label):
            raise ValueError('Both reviewed classes are required')
        w[y == label] *= len(events) / (2 * w[y == label].sum())
    scale = StandardScaler().fit(x, sample_weight=w)
    model = LogisticRegression(C=2, max_iter=3000, random_state=0).fit(scale.transform(x), y, sample_weight=w)
    return dict(mean=scale.mean_.tolist(), scale=scale.scale_.tolist(), weights=model.coef_[0].tolist(), intercept=float(model.intercept_[0]))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('recordings', type=Path)
    parser.add_argument('output', type=Path)
    parser.add_argument('--threshold', type=float, default=0.55)
    args = parser.parse_args()
    sessions = [json.loads(p.read_text()) for p in args.recordings.rglob('session.json')]
    baseline = max(sessions, key=lambda s: s['startedAt'])['model']
    events, conflicts, fixture = extract(args.recordings, baseline)
    results = []
    for session in sorted({e['session'] for e in events}):
        model = fit([e for e in events if e['session'] != session])
        for e in events:
            if e['session'] == session:
                results.append(dict(session=session, label=e['label'], old=clip_score(probability(e['x'], baseline)), held_out=clip_score(probability(e['x'], model))))
    model = fit(events)
    def counts(field, threshold):
        return dict(touches_detected=sum(r['label'] == 1 and r[field] >= threshold for r in results),
                    negative_clips_flagged=sum(r['label'] == 0 and r[field] >= threshold for r in results))
    report = dict(reviewed_clips=len(events), positive_clips=sum(e['label'] for e in events),
                  negative_clips=sum(1-e['label'] for e in events), sessions=len({e['session'] for e in events}),
                  conflicting_clips_excluded=conflicts, threshold=args.threshold,
                  baseline=counts('old', args.threshold), held_out=counts('held_out', args.threshold),
                  comparisons={str(t):counts('held_out',t) for t in (0.35,0.45,0.55)}, clips=results,
                  limitation='Exploratory leave-one-session-out comparison on selected reviewed clips, not unbiased recall or false positives per hour. Unreviewed motion is excluded. Positive window localization uses the frozen baseline model. No prospective validation yet.')
    args.output.mkdir(parents=True, exist_ok=True, mode=0o700)
    for name, data in [('model.json',model),('report.json',report),('parity.json',dict(model=model,samples=(fixture*1000).tolist(),probability=float(probability(np.array([features(fixture)]),model)[0])))]:
        path=args.output/name;path.write_text(json.dumps(data,indent=2));path.chmod(0o600)
    print(json.dumps({k:v for k,v in report.items() if k!='clips'},indent=2))

if __name__ == '__main__':
    main()
