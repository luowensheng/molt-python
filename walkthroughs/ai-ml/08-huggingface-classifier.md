# Hugging Face Text Classifier — text-classifier

This walkthrough builds a text classification API that loads a fine-tuned Hugging Face transformer model and serves predictions over HTTP. The model weights are declared as molt assets and ship inside the binary — no network access required on the inference host. You will download the weights locally, evaluate the model, then ship a single verified artifact to production.

---

## 1. Project initialisation

```bash
molt init text-classifier
cd text-classifier
```

Add dependencies:

```bash
molt add fastapi uvicorn transformers torch accelerate huggingface-hub pydantic
molt add --dev pytest ruff httpx
```

```
Resolving dependencies...
  + fastapi 0.115.5
  + uvicorn 0.32.1
  + transformers 4.47.1
  + torch 2.5.1  (CPU wheel; see GPU note below)
  + accelerate 1.2.1
  + huggingface-hub 0.26.5
  + pydantic 2.10.3
  + pytest 8.3.4 [dev]
  + ruff 0.8.2 [dev]
  + httpx 0.28.0 [dev]
Syncing global store...
  10 packages installed  →  ~/.molt/pkg/  (2.4 GB — PyTorch is large)
Wrote .molt/syspath.json
Wrote .molt/bin/uvicorn, .molt/bin/ruff, .molt/bin/pytest
```

For CUDA:

```bash
# Install the CUDA-enabled torch from the PyTorch index
molt add "torch==2.5.1+cu124" --extra-index-url https://download.pytorch.org/whl/cu124
```

---

## 2. Project layout

```
text-classifier/
├── pyproject.toml
├── uv.lock
├── molt.yaml
├── models/
│   └── sentiment-model/       ← downloaded model lands here
│       ├── config.json
│       ├── tokenizer.json
│       ├── tokenizer_config.json
│       └── model.safetensors
├── text_classifier/
│   ├── __init__.py
│   ├── main.py
│   ├── config.py
│   ├── model.py
│   └── routers/
│       ├── classify.py
│       └── health.py
├── scripts/
│   ├── download_model.py
│   ├── evaluate.py
│   └── benchmark.py
├── data/
│   └── eval_set.csv           ← 500-row labeled eval set
└── tests/
    └── test_classify.py
```

---

## 3. pyproject.toml

```toml
[project]
name = "text-classifier"
version = "1.1.0"
requires-python = ">=3.12"
dependencies = [
  "fastapi>=0.115",
  "uvicorn>=0.32",
  "transformers>=4.47",
  "torch>=2.5",
  "accelerate>=1.2",
  "huggingface-hub>=0.26",
  "pydantic>=2.10",
]

[project.optional-dependencies]
dev = [
  "pytest>=8.3",
  "ruff>=0.8",
  "httpx>=0.28",
]

[tool.molt.tasks]
dev            = "uvicorn text_classifier.main:app --reload --port 8000"
download-model = "python scripts/download_model.py"
evaluate       = "python scripts/evaluate.py --eval-set data/eval_set.csv"
benchmark      = "python scripts/benchmark.py --requests 500 --concurrency 8"
test           = "pytest tests/ -v --tb=short"
lint           = "ruff check ."

[tool.ruff.lint]
select = ["E", "F", "I", "UP"]
```

---

## 4. Downloading the model

**`scripts/download_model.py`**

```python
"""Download a fine-tuned HF model into models/sentiment-model/."""
import os
from pathlib import Path
from huggingface_hub import snapshot_download

MODEL_ID   = os.environ.get("HF_MODEL_ID", "distilbert/distilbert-base-uncased-finetuned-sst-2-english")
MODELS_DIR = Path(__file__).parent.parent / "models" / "sentiment-model"

if MODELS_DIR.exists() and any(MODELS_DIR.iterdir()):
    print(f"Model already present at {MODELS_DIR}")
else:
    print(f"Downloading {MODEL_ID}...")
    snapshot_download(
        repo_id=MODEL_ID,
        local_dir=str(MODELS_DIR),
        ignore_patterns=["*.msgpack", "flax_model*", "tf_model*", "rust_model*"],
    )
    print(f"Downloaded to {MODELS_DIR}")
```

```bash
molt run download-model
```

```
Downloading distilbert/distilbert-base-uncased-finetuned-sst-2-english...
  config.json               1.2 KB   ██████████ 100%
  tokenizer.json            226.0 KB ██████████ 100%
  tokenizer_config.json     1.1 KB   ██████████ 100%
  model.safetensors         267.8 MB ██████████ 100%
Downloaded to models/sentiment-model/
```

---

## 5. Model implementation

**`text_classifier/model.py`**

```python
import os
from functools import lru_cache
from transformers import pipeline, Pipeline

@lru_cache(maxsize=1)
def get_pipeline() -> Pipeline:
    model_dir = os.path.join(
        os.environ.get("MOLT_ROOT", "."),
        "models", "sentiment-model",
    )
    device = int(os.environ.get("CUDA_DEVICE", "-1"))  # -1 = CPU
    print(f"Loading model from {model_dir} (device={'cuda' if device >= 0 else 'cpu'})", flush=True)
    return pipeline(
        "text-classification",
        model=model_dir,
        tokenizer=model_dir,
        device=device,
        truncation=True,
        max_length=512,
    )
```

**`text_classifier/routers/classify.py`**

```python
from fastapi import APIRouter
from pydantic import BaseModel
from text_classifier.model import get_pipeline

router = APIRouter()

class ClassifyRequest(BaseModel):
    text: str | list[str]
    top_k: int = 1

class Prediction(BaseModel):
    label: str
    score: float

@router.post("/classify", response_model=list[list[Prediction]])
def classify(req: ClassifyRequest):
    pipe = get_pipeline()
    texts = req.text if isinstance(req.text, list) else [req.text]
    results = pipe(texts, top_k=req.top_k)
    return results
```

---

## 6. Running in development

```bash
molt run dev
```

```
INFO:     Uvicorn running on http://127.0.0.1:8000 (Press CTRL+C to quit)
Loading model from models/sentiment-model (device=cpu)
Some weights of the model checkpoint at models/sentiment-model were not used when initializing...
INFO:     Application startup complete.
```

Classify some text:

```bash
curl -sf http://localhost:8000/classify \
  -H 'Content-Type: application/json' \
  -d '{"text": ["I love this product!", "This is terrible."], "top_k": 2}' \
  | python -m json.tool
```

```json
[
  [{"label": "POSITIVE", "score": 0.9998}, {"label": "NEGATIVE", "score": 0.0002}],
  [{"label": "NEGATIVE", "score": 0.9994}, {"label": "POSITIVE", "score": 0.0006}]
]
```

Evaluate accuracy against the labeled set:

```bash
molt run evaluate
```

```
Evaluating on data/eval_set.csv (500 examples)...
  [██████████████████████████████████████████] 500/500

Results:
  Accuracy:  92.4%
  F1 (macro): 0.921
  Confusion matrix:
    POSITIVE  NEGATIVE
    228       17        (actual POSITIVE)
    22        233       (actual NEGATIVE)
  Inference time: 0.84s total  (1.68ms / sample)
```

Benchmark throughput:

```bash
molt run benchmark
```

```
Benchmark: 500 requests × concurrency 8  (batch size 1)
  Total time:    14.7s
  Requests/sec:  34.0
  Avg latency:   228ms
  p99 latency:   381ms
  Errors:        0
```

---

## 7. molt.yaml — model weights as assets

```yaml
version: 1

project:
  name: text-classifier
  version: 1.1.0
  python: "3.12"
  description: "Hugging Face text sentiment classifier served as an API"

deps:
  strategy: pyproject

include:
  - "text_classifier/**/*.py"
  - "scripts/evaluate.py"
  - "scripts/benchmark.py"

exclude:
  - "**/__pycache__/"
  - "**/*.pyc"
  - "tests/"

assets:
  files:
    - path: "models/sentiment-model/config.json"
      required: true
      description: "Model architecture config"
    - path: "models/sentiment-model/tokenizer.json"
      required: true
      description: "Tokenizer vocabulary"
    - path: "models/sentiment-model/tokenizer_config.json"
      required: true
    - path: "models/sentiment-model/model.safetensors"
      required: true
      description: "DistilBERT fine-tuned weights (267 MB)"
  max_file_size_mb: 500
  max_total_size_mb: 800

commands:
  default: serve

  serve:
    exec: [uvicorn, "text_classifier.main:app", "--host=0.0.0.0", "--port=8000", "--workers=2"]
    description: "Start the classification API server"

  evaluate:
    exec: [python, "scripts/evaluate.py", "--eval-set", "/data/eval_set.csv"]
    description: "Evaluate model accuracy on a labeled CSV"

  benchmark:
    exec: [python, "scripts/benchmark.py", "--requests=500", "--concurrency=8"]
    description: "Throughput benchmark"

env:
  PYTHONUNBUFFERED: "1"
  CUDA_DEVICE: "-1"

integrity:
  verify_on_install: true
  verify_on_launch: false
```

---

## 8. Building

```bash
molt build
```

```
Building text-classifier v1.1.0 (linux/amd64)...
  Config:    ./molt.yaml
  ✓ Payload: 2,734.1 MB (1893 files)
      code:      8.4 MB  (source + scripts)
      assets:  268.1 MB  (model weights)
      deps:  2,457.6 MB  (torch + transformers)
  ✓ Integrity manifest: text-classifier-v1.1.0.manifest.json
  ✓ Created: text-classifier-v1.1.0 (2,751.4 MB)
    root_hash: 5c2a8f14e7b3d09c6f4a1e8b5c2f9a6d3b0e7f4c1a8e5b2d9f6c3a0e7b4d1f8
    files:     1893   total: 2,734.1 MB
    build time: 28.4s
```

Use `molt diff` to verify a model swap:

```bash
molt diff ./text-classifier-v1.0.0 ./text-classifier-v1.1.0
```

```
Changed: 1 file(s)
  ~ models/sentiment-model/model.safetensors (-3.2 MB, updated weights)
Changed: 2 file(s)
  ~ text_classifier/routers/classify.py (+88 bytes)
  ~ scripts/evaluate.py (+14 bytes)
Total size change: -3.1 MB (-3.1 MB)
```

---

## 9. Deploy to another machine

```bash
scp text-classifier-v1.1.0 text-classifier-v1.1.0.manifest.json app@api-01:/opt/classifiers/
ssh app@api-01
```

```bash
/opt/classifiers/text-classifier-v1.1.0 install
```

```
Installing text-classifier v1.1.0...
  Verifying integrity...  ✓ root_hash match
  Extracting payload (1893 files)...
    [2,734 MB] ████████████████████████████████ 100%
  ✓ Installed to /home/app/.molt/apps/text-classifier/1.1.0/
```

```bash
CUDA_DEVICE=0 /opt/classifiers/text-classifier-v1.1.0 run
```

```
Loading model from /home/app/.molt/apps/text-classifier/1.1.0/models/sentiment-model (device=cuda:0)
INFO:     Application startup complete.
INFO:     Uvicorn running on http://0.0.0.0:8000
```

---

## 10. Tips

- **Model versioning via asset hashes**: the integrity manifest records the SHA-256 of every asset file, including `model.safetensors`. Use `molt diff v1.0.0 v1.1.0` to confirm that a "bug fix" release didn't silently swap the model weights.
- **`MOLT_ROOT` for model path**: `text_classifier/model.py` resolves the model directory via `$MOLT_ROOT`, so the same code works in development (`MOLT_ROOT` unset, defaults to `.`) and in production (set to the install path by the launcher).
- **CPU vs GPU wheels**: the `torch` wheel is CPU-only by default. For a GPU server, build on a machine with CUDA available and install the CUDA-enabled wheel. The binary will be platform-specific but ship with the right `.so` files.
- **Batch inference**: the `/classify` endpoint accepts a list of texts and processes them in a single pipeline call. Bump `--concurrency` in the benchmark to find the sweet spot for your hardware.
- **Smaller models**: replace DistilBERT with `distilbert-base-uncased` + a custom head for sub-100 MB weights. The asset size drops proportionally and the binary becomes more practical to distribute over slow links.
