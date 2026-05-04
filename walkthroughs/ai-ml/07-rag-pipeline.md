# RAG Pipeline Service — rag-service

This walkthrough builds a retrieval-augmented generation (RAG) service: a FastAPI API that answers questions about your documents using OpenAI embeddings, a local ChromaDB vector store, and LangChain for orchestration. A separate `ingest` command loads documents into the vector database. You will run both the ingest pipeline and the query API locally under molt, then ship them as a single binary for production deployment.

---

## 1. Project initialisation

```bash
molt init rag-service
cd rag-service
```

Add dependencies:

```bash
molt add fastapi uvicorn openai langchain langchain-openai langchain-community \
         chromadb tiktoken sentence-transformers
molt add --dev pytest ruff httpx
```

```
Resolving dependencies...
  + fastapi 0.115.5
  + uvicorn 0.32.1
  + openai 1.57.2
  + langchain 0.3.9
  + langchain-openai 0.2.10
  + langchain-community 0.3.9
  + chromadb 0.5.20
  + tiktoken 0.8.0
  + sentence-transformers 3.3.1
  + pytest 8.3.4 [dev]
  + ruff 0.8.2 [dev]
  + httpx 0.28.0 [dev]
Syncing global store...
  13 packages installed  →  ~/.molt/pkg/  (1.2 GB — sentence-transformers includes model)
Wrote .molt/syspath.json
Wrote .molt/bin/uvicorn, .molt/bin/chroma, .molt/bin/ruff, .molt/bin/pytest
```

---

## 2. Project layout

```
rag-service/
├── pyproject.toml
├── uv.lock
├── molt.yaml
├── rag_service/
│   ├── __init__.py
│   ├── main.py
│   ├── config.py
│   ├── vectorstore.py
│   ├── retriever.py
│   └── routers/
│       ├── query.py
│       └── health.py
├── scripts/
│   ├── ingest.py
│   └── benchmark.py
├── docs/                      ← raw documents to ingest
│   ├── handbook.pdf
│   └── policies.md
└── tests/
    ├── conftest.py
    └── test_query.py
```

---

## 3. pyproject.toml

```toml
[project]
name = "rag-service"
version = "0.5.0"
requires-python = ">=3.12"
dependencies = [
  "fastapi>=0.115",
  "uvicorn>=0.32",
  "openai>=1.57",
  "langchain>=0.3",
  "langchain-openai>=0.2",
  "langchain-community>=0.3",
  "chromadb>=0.5",
  "tiktoken>=0.8",
  "sentence-transformers>=3.3",
]

[project.optional-dependencies]
dev = [
  "pytest>=8.3",
  "ruff>=0.8",
  "httpx>=0.28",
]

[tool.molt.tasks]
dev       = "uvicorn rag_service.main:app --reload --port 8000"
ingest    = "python scripts/ingest.py --docs-dir docs/"
test      = "pytest tests/ -v --tb=short"
lint      = "ruff check ."
benchmark = "python scripts/benchmark.py --queries 50"

[tool.ruff.lint]
select = ["E", "F", "I", "UP"]
```

---

## 4. Application code

**`rag_service/config.py`**

```python
import os
from pydantic_settings import BaseSettings

class Settings(BaseSettings):
    openai_api_key: str
    chroma_persist_dir: str = os.path.join(
        os.environ.get("MOLT_ROOT", "."), "data", "chroma"
    )
    collection_name: str = "documents"
    embedding_model: str = "text-embedding-3-small"
    llm_model: str = "gpt-4o-mini"
    retrieval_k: int = 5

    class Config:
        env_file = ".env"

settings = Settings()
```

**`rag_service/vectorstore.py`**

```python
from functools import lru_cache
import chromadb
from langchain_openai import OpenAIEmbeddings
from langchain_community.vectorstores import Chroma
from rag_service.config import settings

@lru_cache(maxsize=1)
def get_vectorstore() -> Chroma:
    client = chromadb.PersistentClient(path=settings.chroma_persist_dir)
    embeddings = OpenAIEmbeddings(
        model=settings.embedding_model,
        openai_api_key=settings.openai_api_key,
    )
    return Chroma(
        client=client,
        collection_name=settings.collection_name,
        embedding_function=embeddings,
    )
```

**`rag_service/retriever.py`**

```python
from langchain_openai import ChatOpenAI
from langchain.chains import RetrievalQA
from langchain.prompts import PromptTemplate
from rag_service.vectorstore import get_vectorstore
from rag_service.config import settings

PROMPT = PromptTemplate(
    input_variables=["context", "question"],
    template=(
        "Use the following context to answer the question. "
        "If you don't know, say so.\n\n"
        "Context:\n{context}\n\n"
        "Question: {question}\n\n"
        "Answer:"
    ),
)

def get_chain() -> RetrievalQA:
    llm = ChatOpenAI(model=settings.llm_model, temperature=0.1, openai_api_key=settings.openai_api_key)
    retriever = get_vectorstore().as_retriever(search_kwargs={"k": settings.retrieval_k})
    return RetrievalQA.from_chain_type(
        llm=llm,
        retriever=retriever,
        chain_type_kwargs={"prompt": PROMPT},
        return_source_documents=True,
    )
```

**`rag_service/routers/query.py`**

```python
from fastapi import APIRouter
from pydantic import BaseModel
from rag_service.retriever import get_chain

router = APIRouter()

class QueryRequest(BaseModel):
    question: str

@router.post("/query")
def query(req: QueryRequest):
    chain = get_chain()
    result = chain.invoke({"query": req.question})
    sources = [doc.metadata.get("source", "unknown") for doc in result["source_documents"]]
    return {"answer": result["result"], "sources": list(set(sources))}
```

---

## 5. Running the ingest pipeline

```bash
OPENAI_API_KEY=sk-... molt run ingest
```

```
Loading documents from docs/...
  handbook.pdf   → 142 chunks
  policies.md    → 38 chunks
  Total: 180 chunks

Embedding 180 chunks with text-embedding-3-small...
  [██████████████████████████████████████████] 180/180

Persisting to data/chroma/documents...
  ✓ 180 documents stored
  Collection size: 180 vectors (1536 dims)
Ingest complete in 12.4s
```

Start the API server:

```bash
OPENAI_API_KEY=sk-... molt run dev
```

```
INFO:     Uvicorn running on http://127.0.0.1:8000 (Press CTRL+C to quit)
INFO:     Application startup complete.
```

Ask a question:

```bash
curl -sf http://localhost:8000/query \
  -H 'Content-Type: application/json' \
  -d '{"question": "What is the remote work policy?"}' \
  | python -m json.tool
```

```json
{
  "answer": "According to the policies document, employees may work remotely up to 3 days per week with manager approval. ...",
  "sources": ["docs/policies.md"]
}
```

Run tests:

```bash
molt run test
```

```
========================= test session starts ==========================
collected 11 items

tests/test_query.py::test_query_returns_answer PASSED
tests/test_query.py::test_query_with_sources PASSED
tests/test_query.py::test_query_unknown_topic PASSED
tests/test_query.py::test_health_endpoint PASSED
...
============================== 11 passed in 2.31s ==============================
```

---

## 6. molt.yaml

```yaml
version: 1

project:
  name: rag-service
  version: 0.5.0
  python: "3.12"
  description: "RAG query service with ChromaDB and OpenAI"

deps:
  strategy: pyproject

include:
  - "rag_service/**/*.py"
  - "scripts/ingest.py"
  - "scripts/benchmark.py"

exclude:
  - "**/__pycache__/"
  - "**/*.pyc"
  - "tests/"
  - "docs/"

commands:
  default: serve

  serve:
    exec: [uvicorn, "rag_service.main:app", "--host=0.0.0.0", "--port=8000", "--workers=2"]
    description: "Start the RAG query API server"

  ingest:
    exec: [python, "scripts/ingest.py", "--docs-dir", "/data/documents"]
    description: "Load documents from /data/documents into ChromaDB"

  benchmark:
    exec: [python, "scripts/benchmark.py", "--queries=50"]
    description: "Run retrieval quality benchmark"

env:
  PYTHONUNBUFFERED: "1"

integrity:
  verify_on_install: true
```

---

## 7. Building

```bash
molt build
```

```
Building rag-service v0.5.0 (linux/amd64)...
  Config:    ./molt.yaml
  ✓ Payload: 1,187.3 MB (2341 files)
  ✓ Integrity manifest: rag-service-v0.5.0.manifest.json
  ✓ Created: rag-service-v0.5.0 (1,203.8 MB)
    root_hash: 6f3a8d2c1e9b7f4a0d6c3f8a2e9b7f4a1d6c3f8a2e9b7f4a0d6c3f8a2e9b7f4a
    files:     2341   total: 1,187.3 MB
    build time: 18.7s
```

---

## 8. Deploy to a Linux server

```bash
scp rag-service-v0.5.0 rag-service-v0.5.0.manifest.json app@rag-01:/opt/rag/
ssh app@rag-01
```

```bash
OPENAI_API_KEY="$(cat /run/secrets/openai_key)" \
CHROMA_PERSIST_DIR="/var/rag/chroma" \
  /opt/rag/rag-service-v0.5.0 install
```

```
Installing rag-service v0.5.0...
  Verifying integrity...  ✓ root_hash match
  Extracting payload (2341 files)...
  ✓ Installed to /home/app/.molt/apps/rag-service/0.5.0/
```

Run ingest against your production documents:

```bash
OPENAI_API_KEY="$(cat /run/secrets/openai_key)" \
CHROMA_PERSIST_DIR="/var/rag/chroma" \
  /opt/rag/rag-service-v0.5.0 run ingest
```

Start the query server:

```bash
OPENAI_API_KEY="$(cat /run/secrets/openai_key)" \
CHROMA_PERSIST_DIR="/var/rag/chroma" \
  /opt/rag/rag-service-v0.5.0 run serve
```

---

## 9. Tips

- **`MOLT_ROOT` for the Chroma path**: the default `chroma_persist_dir` resolves to `$MOLT_ROOT/data/chroma` at runtime, so the vector store lands inside the install directory. Override with `CHROMA_PERSIST_DIR` in production to keep data separate from the binary across version upgrades.
- **Ingest as a separate step**: the `ingest` command runs from the same binary but is called once per document batch, not at startup. Wire it into your document CI pipeline — commit new docs → trigger `ingest` → restart serve.
- **Sentence-transformer model size**: `sentence-transformers` ships with pre-trained model weights. These are included in the `~/.molt/pkg/` store and embedded in the binary, making the 1.2 GB binary self-contained — no internet access needed on the inference host.
- **Retrieval quality benchmark**: `molt run benchmark` computes recall@5 against a golden Q&A set. Run it before deploying a new document batch or upgrading the embedding model.
- **Local embeddings**: replace `OpenAIEmbeddings` with `langchain_community.embeddings.HuggingFaceEmbeddings` using `all-MiniLM-L6-v2` to eliminate the OpenAI API dependency entirely and run fully offline.
