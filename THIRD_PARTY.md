# Third-party software

The MIT license in this repository applies only to original project code.
Dependencies remain under their respective licenses.

Important runtime components include:

- PyTorch, Transformers, librosa and their Python dependencies;
- the CLAP model and model weights downloaded from their upstream provider;
- modernc SQLite and other Go modules listed in `player/go.sum`;
- Flutter and plugins listed in `mobile/flutter/pubspec.lock`;
- ffmpeg supplied by the host or container base distribution.

This repository does not intentionally vendor model weights, music, ffmpeg
binaries or package-manager caches. Before redistributing a container image or
mobile binary, review the exact licenses of the resolved dependency versions
and the selected model weights. An ffmpeg build may enable optional codecs
with additional licensing requirements.
