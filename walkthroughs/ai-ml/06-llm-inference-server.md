# LLM Inference Server — inference-server

This walkthrough builds a local LLM inference API using `llama-cpp-python` to serve quantised GGUF models via a FastAPI endpoint. The server exposes an OpenAI-compatible `/v1/chat/completions` endpoint so existing client code works without modification. Model weights are declared as molt assets and ship inside the hermetic binary — making the entire service, including the multi-gigabyte model, a single self-verifying artifact you can `scp` to a GPU server.

---

## 1. Project initialisation

```bash
molt init inference-server
cd inference-server
```

Add dependencies:

```bash
molt add fastapi uvicorn "llama-cpp-python[server]" huggingface-hub pydantic
molt add --dev pytest ruff httpx
```

```
Resolving dependencies...
  + fastapi 0.115.5
  + uvicorn 0.32.1
  + llama-cpp-python 0.3.2  (CPU build; GPU flags set via CMAKE_ARGS)
  + huggingface-hub 0.26.5
  + pydantic 2.10.3
  + pytest 8.3.4 [dev]
  + ruff 0.8.2 [dev]
  + httpx 0.28.0 [dev]
Syncing global store...
  8 packages installed  →  ~/.molt/pkg/  (31.4 MB, llama-cpp-python includes compiled .so)
Wrote .molt/syspath.json
Wrote .molt/bin/uvicorn, .molt/bin/ruff, .molt/bin/pytest
```

For GPU builds, set `CMAKE_ARGS` before `molt add`:

```bash
# CUDA (NVIDIA)
CMAKE_ARGS="-DGGML_CUDA=on" molt add llama-cpp-python

# Metal (Apple Silicon)
CMAKE_ARGS="-DGGML_METAL=on" molt add llama-cpp-python
```

---

## 2. Project layout

```
inference-server/
├── pyproject.toml
├── uv.lock
├── molt.yaml
├── models/                    ← model weights land here after download
│   └── .gitkeep
├── inference_server/
│   ├── __init__.py
│   ├── main.py
│   ├── config.py
│   ├── llm.py
│   └── routers/
│       ├── completions.py
│       └── health.py
├── scripts/
│   └── download_model.py
└── tests/
    └── test_completions.py
```

---

## 3. pyproject.toml

```toml
[project]
name = "inference-server"
version = "1.0.0"
requires-python = ">=3.12"
dependencies = [
  "fastapi>=0.115",
  "uvicorn>=0.32",
  "llama-cpp-python>=0.3",
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
dev            = "uvicorn inference_server.main:app --reload --port 8000"
test           = "pytest tests/ -v --tb=short"
lint           = "ruff check ."
download-model = "python scripts/download_model.py"
benchmark      = "python scripts/bench.py --requests 100 --concurrency 4"

[tool.ruff.lint]
select = ["E", "F", "I", "UP"]
```

---

## 4. Downloading the model

**`scripts/download_model.py`**

```python
"""Download a quantised GGUF model from Hugging Face into models/."""
import os
from pathlib import Path
from huggingface_hub import hf_hub_download

REPO_ID   = os.environ.get("HF_REPO",   "TheBloke/Mistral-7B-Instruct-v0.2-GGUF")
FILENAME  = os.environ.get("HF_FILE",   "mistral-7b-instruct-v0.2.Q4_K_M.gguf")
MODELS_DIR = Path(__file__).parent.parent / "models"
MODELS_DIR.mkdir(exist_ok=True)

dest = MODELS_DIR / FILENAME
if dest.exists():
    print(f"Already downloaded: {dest}")
else:
    print(f"Downloading {FILENAME} from {REPO_ID}...")
    hf_hub_download(repo_id=REPO_ID, filename=FILENAME, local_dir=str(MODELS_DIR))
    print(f"Saved to {dest}")
```

```bash
molt run download-model
```

```
Downloading mistral-7b-instruct-v0.2.Q4_K_M.gguf from TheBloke/Mistral-7B-Instruct-v0.2-GGUF...
  4.37 GB ████████████████████████████████████████ 100%
Saved to models/mistral-7b-instruct-v0.2.Q4_K_M.gguf
```

---

## 5. Server implementation

**`inference_server/llm.py`**

```python
import os
from functools import lru_cache
from llama_cpp import Llama

@lru_cache(maxsize=1)
def get_model() -> Llama:
    model_path = os.environ.get(
        "MODEL_PATH",
        os.path.join(os.environ.get("MOLT_ROOT", "."), "models", "mistral-7b-instruct-v0.2.Q4_K_M.gguf"),
    )
    print(f"Loading model from {model_path}", flush=True)
    return Llama(
        model_path=model_path,
        n_ctx=4096,
        n_gpu_layers=int(os.environ.get("N_GPU_LAYERS", "0")),
        verbose=False,
    )
```

**`inference_server/routers/completions.py`** (excerpt)

```python
from fastapi import APIRouter
from pydantic import BaseModel
from inference_server.llm import get_model

router = APIRouter()

class ChatMessage(BaseModel):
    role: str
    content: str

class ChatRequest(BaseModel):
    model: str = "local"
    messages: list[ChatMessage]
    max_tokens: int = 512
    temperature: float = 0.7
    stream: bool = False

@router.post("/v1/chat/completions")
def chat_completions(req: ChatRequest):
    llm = get_model()
    prompt = "\n".join(f"<|{m.role}|>\n{m.content}" for m in req.messages) + "\n<|assistant|>\n"
    result = llm(prompt, max_tokens=req.max_tokens, temperature=req.temperature, stop=["<|user|>"])
    return {
        "id": "chatcmpl-local",
        "object": "chat.completion",
        "model": req.model,
        "choices": [{"message": {"role": "assistant", "content": result["choices"][0]["text"]}, "finish_reason": "stop"}],
        "usage": result["usage"],
    }
```

---

## 6. Running in development

```bash
molt run dev
```

```
INFO:     Will watch for changes in these directories: ['/home/dev/inference-server']
INFO:     Uvicorn running on http://127.0.0.1:8000 (Press CTRL+C to quit)
Loading model from models/mistral-7b-instruct-v0.2.Q4_K_M.gguf
llama_model_load_internal: loaded meta data with 24 key-value pairs
llama_model_load_internal: format     = GGUF V3
llama_model_load_internal: model type = 7B
llama_model_load_internal: model size = 4.37 GB (Q4_K_M quantisation)
```

Test a completion:

```bash
curl -sf http://localhost:8000/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"messages":[{"role":"user","content":"What is the capital of France?"}]}' \
  | python -m json.tool
```

```json
{
  "id": "chatcmpl-local",
  "object": "chat.completion",
  "model": "local",
  "choices": [
    {
      "message": {"role": "assistant", "content": "The capital of France is Paris."},
      "finish_reason": "stop"
    }
  ],
  "usage": {"prompt_tokens": 18, "completion_tokens": 9, "total_tokens": 27}
}
```

Run the benchmark:

```bash
molt run benchmark
```

```
Benchmark: 100 requests × concurrency 4
  Model: mistral-7b-instruct-v0.2.Q4_K_M.gguf (CPU, 4 threads)

  Total time:       47.3s
  Requests/sec:     2.11
  Avg tokens/s:     14.8 tok/s
  Avg latency:      1.89s (first token)
  p99 latency:      3.41s
  Errors:           0
```

---

## 7. molt.yaml — shipping model weights as assets

```yaml
version: 1

project:
  name: inference-server
  version: 1.0.0
  python: "3.12"
  description: "Local LLM inference server (llama-cpp-python + GGUF)"

deps:
  strategy: pyproject

include:
  - "inference_server/**/*.py"

exclude:
  - "**/__pycache__/"
  - "**/*.pyc"
  - "tests/"
  - "scripts/"

assets:
  files:
    - path: "models/mistral-7b-instruct-v0.2.Q4_K_M.gguf"
      required: true
      description: "Mistral 7B Instruct Q4_K_M quantised weights"
  max_file_size_mb: 5000
  max_total_size_mb: 6000

commands:
  default: serve

  serve:
    exec: [uvicorn, "inference_server.main:app", "--host=0.0.0.0", "--port=8000", "--workers=1"]
    description: "Start the inference API server"
    env:
      PYTHONUNBUFFERED: "1"

  download:
    exec: [python, "scripts/download_model.py"]
    description: "Download model weights from Hugging Face"

  benchmark:
    exec: [python, "scripts/bench.py", "--requests=50", "--concurrency=2"]
    description: "Quick throughput benchmark"

env:
  PYTHONUNBUFFERED: "1"
  N_GPU_LAYERS: "0"

integrity:
  verify_on_install: true
  algorithm: sha256
```

---

## 8. Building

```bash
molt build
```

```
Building inference-server v1.0.0 (linux/amd64)...
  Config:    ./molt.yaml
  ✓ Payload: 4,621.4 MB (1089 files)
      code:    28.6 MB  (1087 files, include globs)
      assets:  4,592.8 MB (1 file — model weights)
  ✓ Integrity manifest: inference-server-v1.0.0.manifest.json
  ✓ Created: inference-server-v1.0.0 (4,638.2 MB)
    root_hash: 3e1c9a7d5f2b8e04a6c3f0d9b7e4a1c8d5f2b9e6a3c0f7d4b1e8a5c2d9f6b3e0
    files:     1089   total: 4,621.4 MB
    build time: 38.2s
```

---

## 9. Deploy to a GPU server

```bash
# Ship the ~4.6 GB binary (one-time; model weights are embedded)
scp inference-server-v1.0.0 inference-server-v1.0.0.manifest.json ml@gpu-srv-01:/opt/models/

ssh ml@gpu-srv-01
```

```bash
N_GPU_LAYERS=35 /opt/models/inference-server-v1.0.0 install
```

```
Installing inference-server v1.0.0...
  Verifying integrity...  ✓ root_hash match
  Extracting payload (1089 files)...
    [4,621 MB] ████████████████████████████████ 100%
  Setting up Python environment...
  ✓ Installed to /home/ml/.molt/apps/inference-server/1.0.0/
```

```bash
N_GPU_LAYERS=35 /opt/models/inference-server-v1.0.0 run
```

```
Loading model from /home/ml/.molt/apps/inference-server/1.0.0/models/mistral-7b-instruct-v0.2.Q4_K_M.gguf
ggml_cuda_init: GGML_CUDA_FORCE_MMQ = 0
ggml_cuda_init: found 1 CUDA device: NVIDIA A10G (24564 MiB)
llama_model_load_internal: offloading 35 repeating layers to GPU
INFO:     Application startup complete.
INFO:     Uvicorn running on http://0.0.0.0:8000
```

---

## 10. Tips

- **`MOLT_ROOT` env var**: the launcher sets `MOLT_ROOT` to the install directory at runtime. Use it in `llm.py` to find the model path without hardcoding anything. The fallback to `"."` makes it work in development too.
- **Large binary transfers**: use `rsync -P` for resumable uploads of multi-gigabyte binaries. Once the GPU server has the binary installed, subsequent version upgrades only need to re-transfer diffs if the model hasn't changed — use `molt diff` to confirm.
- **Model not in the binary**: if the model weights change frequently or are too large to embed, omit them from `assets`, remove `required: true`, and pass `MODEL_PATH` at runtime pointing to a pre-downloaded file on the server.
- **GPU layer tuning**: `N_GPU_LAYERS` controls how many transformer layers are offloaded to the GPU. Start with `35` for a 7B model on a 24 GB card; increase towards `40` if VRAM allows.
- **`verify_on_install: true`** re-hashes all 4.6 GB on install. This takes ~10 seconds but guarantees the multi-gigabyte model file was not corrupted in transit. Set `MOLT_SKIP_VERIFY=1` to bypass in low-trust environments only.
