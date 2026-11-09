# auto-pelican attribution

Source: https://github.com/haowang02/auto-pelican
Pinned commit: `8f152aaeff6913a018d3edd5a696b438c47853ee`
License: MIT (see LICENSE).

`animation.py`, `svg.py`, `judge.py`, and `prompts.py` are copied unchanged
from that commit. `worker.py` is our bounded stdin/stdout adapter. It reuses
the original animation sampler, renderer, checklist, scoring caps and weights.
It does not run the generator CLI, produce GIFs, or modify account routing.
It adds input/resource limits, limits API retries and output size, and returns
rendering/transport failures as unscored. The original pelican-only prompt is
used when visual scoring is enabled. Scores are observations, not evidence of
model identity or general capability.
