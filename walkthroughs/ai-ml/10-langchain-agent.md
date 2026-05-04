# LangChain ReAct Agent — research-agent

This walkthrough builds an interactive research agent using LangChain's ReAct loop. The agent can search the web via DuckDuckGo, query a FAISS vector store of reference documents, perform basic calculations, and execute sandboxed Python. It is exposed as both a REST API (`/invoke`) and an interactive REPL (`molt run chat`). You will run it locally under molt, then ship it as a hermetic binary for deployment on a remote server.

---

## 1. Project initialisation

```bash
molt init research-agent
cd research-agent
```

Add dependencies:

```bash
molt add langchain langchain-openai langchain-community faiss-cpu duckduckgo-search
molt add --dev pytest ruff httpx
```

```
Resolving dependencies...
  + langchain 0.3.9
  + langchain-openai 0.2.10
  + langchain-community 0.3.9
  + faiss-cpu 1.9.0
  + duckduckgo-search 7.2.1
  + pytest 8.3.4 [dev]
  + ruff 0.8.2 [dev]
  + httpx 0.28.0 [dev]
Syncing global store...
  9 packages installed  →  ~/.molt/pkg/  (147.3 MB)
Wrote .molt/syspath.json
Wrote .molt/bin/ruff, .molt/bin/pytest
```

---

## 2. Project layout

```
research-agent/
├── pyproject.toml
├── uv.lock
├── molt.yaml
├── research_agent/
│   ├── __init__.py
│   ├── main.py
│   ├── config.py
│   ├── agent.py
│   ├── tools.py
│   ├── vectorstore.py
│   └── routers/
│       ├── invoke.py
│       └── health.py
├── scripts/
│   ├── chat.py         ← interactive REPL
│   └── ingest_docs.py
├── docs/               ← reference documents for the vector store
│   └── research_papers.md
└── tests/
    └── test_agent.py
```

---

## 3. pyproject.toml

```toml
[project]
name = "research-agent"
version = "0.2.0"
requires-python = ">=3.12"
dependencies = [
  "langchain>=0.3",
  "langchain-openai>=0.2",
  "langchain-community>=0.3",
  "faiss-cpu>=1.9",
  "duckduckgo-search>=7.2",
  "fastapi>=0.115",
  "uvicorn>=0.32",
]

[project.optional-dependencies]
dev = [
  "pytest>=8.3",
  "ruff>=0.8",
  "httpx>=0.28",
]

[tool.molt.tasks]
dev  = "uvicorn research_agent.main:app --reload --port 8000"
test = "pytest tests/ -v --tb=short"
lint = "ruff check ."
chat = "python scripts/chat.py"

[tool.ruff.lint]
select = ["E", "F", "I", "UP"]
```

---

## 4. Tools definition

**`research_agent/tools.py`**

```python
import math
from langchain.tools import Tool
from langchain_community.tools.ddg_search import DuckDuckGoSearchRun
from research_agent.vectorstore import get_retriever

def _calculator(expression: str) -> str:
    """Evaluate a safe mathematical expression."""
    allowed = set("0123456789 +-*/().^ ")
    if not all(c in allowed for c in expression):
        return "Error: only basic arithmetic is supported."
    try:
        result = eval(expression, {"__builtins__": None, "math": math}, {})  # noqa: S307
        return str(result)
    except Exception as e:
        return f"Error: {e}"

def _doc_search(query: str) -> str:
    """Search the local document vector store."""
    retriever = get_retriever()
    docs = retriever.invoke(query)
    if not docs:
        return "No relevant documents found."
    return "\n\n---\n\n".join(
        f"[{doc.metadata.get('source', 'doc')}]\n{doc.page_content}" for doc in docs[:3]
    )

def get_tools() -> list[Tool]:
    return [
        DuckDuckGoSearchRun(name="web_search", description="Search the web for current information."),
        Tool(name="calculator", func=_calculator,
             description="Evaluate a math expression. Input: '2 * (3 + 4)'"),
        Tool(name="document_search", func=_doc_search,
             description="Search local research documents by semantic similarity."),
    ]
```

---

## 5. Agent setup

**`research_agent/agent.py`**

```python
from functools import lru_cache
from langchain.agents import AgentExecutor, create_react_agent
from langchain_openai import ChatOpenAI
from langchain import hub
from research_agent.config import settings
from research_agent.tools import get_tools

@lru_cache(maxsize=1)
def get_agent_executor() -> AgentExecutor:
    llm = ChatOpenAI(
        model=settings.model,
        temperature=0,
        openai_api_key=settings.openai_api_key,
    )
    tools = get_tools()
    prompt = hub.pull("hwchase17/react")
    agent = create_react_agent(llm=llm, tools=tools, prompt=prompt)
    return AgentExecutor(
        agent=agent,
        tools=tools,
        verbose=True,
        max_iterations=8,
        handle_parsing_errors=True,
    )
```

**`research_agent/routers/invoke.py`**

```python
from fastapi import APIRouter
from pydantic import BaseModel
from research_agent.agent import get_agent_executor

router = APIRouter()

class InvokeRequest(BaseModel):
    input: str
    session_id: str | None = None

@router.post("/invoke")
def invoke(req: InvokeRequest):
    executor = get_agent_executor()
    result = executor.invoke({"input": req.input})
    return {"output": result["output"], "intermediate_steps": len(result.get("intermediate_steps", []))}
```

---

## 6. Interactive REPL

**`scripts/chat.py`**

```python
#!/usr/bin/env python
"""Interactive REPL for the research agent."""
import os, sys
sys.path.insert(0, os.path.join(os.environ.get("MOLT_ROOT", "."), "research_agent"))

from research_agent.agent import get_agent_executor

def main() -> None:
    print("Research Agent — type 'quit' to exit.\n")
    executor = get_agent_executor()
    while True:
        try:
            user_input = input("You: ").strip()
        except (EOFError, KeyboardInterrupt):
            print("\nGoodbye.")
            break
        if user_input.lower() in ("quit", "exit", "q"):
            break
        if not user_input:
            continue
        result = executor.invoke({"input": user_input})
        print(f"\nAgent: {result['output']}\n")

if __name__ == "__main__":
    main()
```

---

## 7. Running in development

Start the API server:

```bash
OPENAI_API_KEY=sk-... molt run dev
```

```
INFO:     Uvicorn running on http://127.0.0.1:8000 (Press CTRL+C to quit)
INFO:     Application startup complete.
```

Call the agent:

```bash
curl -sf http://localhost:8000/invoke \
  -H 'Content-Type: application/json' \
  -d '{"input": "What is the current price of gold per ounce? Also, if I have 15.7 ounces, how much is that worth?"}' \
  | python -m json.tool
```

```
> Entering new AgentExecutor chain...
Thought: I need to search for the current gold price, then calculate.
Action: web_search
Action Input: current gold price per ounce USD 2025
Observation: Gold (XAU/USD) is trading at $2,641.30 per troy ounce as of December 2025.
Thought: Now I can calculate 15.7 × 2641.30
Action: calculator
Action Input: 15.7 * 2641.30
Observation: 41468.41

> Finished chain.
```

```json
{
  "output": "The current gold price is approximately $2,641.30 per troy ounce. Your 15.7 ounces would be worth $41,468.41.",
  "intermediate_steps": 2
}
```

Start the interactive REPL:

```bash
OPENAI_API_KEY=sk-... molt run chat
```

```
Research Agent — type 'quit' to exit.

You: Summarise the key findings from our research documents on transformer attention mechanisms.

> Entering new AgentExecutor chain...
Action: document_search
Action Input: transformer attention mechanisms key findings
Observation: [research_papers.md]
Multi-head attention allows the model to jointly attend to information from ...

Agent: The key findings from your research documents on transformer attention mechanisms are: ...

You: quit
Goodbye.
```

Run tests:

```bash
molt run test
```

```
========================= test session starts ==========================
collected 7 items

tests/test_agent.py::test_calculator_tool PASSED
tests/test_agent.py::test_doc_search_tool PASSED
tests/test_agent.py::test_invoke_endpoint PASSED
tests/test_agent.py::test_health_endpoint PASSED
...
============================== 7 passed in 1.44s ==============================
```

---

## 8. molt.yaml

```yaml
version: 1

project:
  name: research-agent
  version: 0.2.0
  python: "3.12"
  description: "LangChain ReAct agent — web search, calculator, document QA"

deps:
  strategy: pyproject

include:
  - "research_agent/**/*.py"
  - "scripts/chat.py"
  - "scripts/ingest_docs.py"

exclude:
  - "**/__pycache__/"
  - "**/*.pyc"
  - "tests/"
  - "docs/"

commands:
  default: serve

  serve:
    exec: [uvicorn, "research_agent.main:app", "--host=0.0.0.0", "--port=8000", "--workers=2"]
    description: "Run the agent REST API server"
    env:
      PYTHONUNBUFFERED: "1"

  chat:
    exec: [python, "scripts/chat.py"]
    description: "Interactive REPL for the research agent"

  ingest:
    exec: [python, "scripts/ingest_docs.py", "--docs-dir=/data/docs"]
    description: "Load documents into the local FAISS vector store"

env:
  PYTHONUNBUFFERED: "1"

integrity:
  verify_on_install: true
```

---

## 9. Building

```bash
molt build
```

```
Building research-agent v0.2.0 (linux/amd64)...
  Config:    ./molt.yaml
  ✓ Payload: 152.4 MB (1831 files)
  ✓ Integrity manifest: research-agent-v0.2.0.manifest.json
  ✓ Created: research-agent-v0.2.0 (168.7 MB)
    root_hash: 9a4d2c1f8e6b0a3d5f7c2a9e4d1c8b5f2a9e6d3c0f7b4a1e8d5c2f9a6d3b0e7
    files:     1831   total: 152.4 MB
    build time: 7.4s
```

---

## 10. Deploy to another machine

```bash
scp research-agent-v0.2.0 research-agent-v0.2.0.manifest.json app@agent-01:/opt/agents/
ssh app@agent-01
```

```bash
OPENAI_API_KEY="$(cat /run/secrets/openai_key)" \
  /opt/agents/research-agent-v0.2.0 install
```

```
Installing research-agent v0.2.0...
  Verifying integrity...  ✓ root_hash match
  Extracting payload (1831 files)...
  ✓ Installed to /home/app/.molt/apps/research-agent/0.2.0/
```

Start the API server:

```bash
OPENAI_API_KEY="$(cat /run/secrets/openai_key)" \
  /opt/agents/research-agent-v0.2.0 run serve
```

```
INFO:     Uvicorn running on http://0.0.0.0:8000
INFO:     Application startup complete.
```

Use the interactive REPL directly on the server (useful for debugging):

```bash
OPENAI_API_KEY="$(cat /run/secrets/openai_key)" \
  /opt/agents/research-agent-v0.2.0 run chat
```

```
Research Agent — type 'quit' to exit.

You: What version of langchain is installed?

> Entering new AgentExecutor chain...
Action: calculator
Action Input: 0.3 + 0.0
Observation: 0.3

Agent: LangChain 0.3.9 is installed in this environment.
```

---

## 11. Tips

- **Tool extensibility**: add new tools by appending to the list returned by `get_tools()`. Each tool is a regular Python function decorated with `Tool(name=..., func=..., description=...)`. Rebuild the binary to ship updated tools.
- **FAISS vector store persistence**: by default the FAISS index is built in memory on first use. Persist it to disk with `vectorstore.save_local("faiss-index")` in `ingest_docs.py` and load it with `FAISS.load_local(...)` in `vectorstore.py`. Include the persisted index as a molt asset for offline deployments.
- **Prompt caching**: LangChain's hub prompt (`hwchase17/react`) is downloaded at first use. Cache it by committing the pulled prompt string to a file in the repo and loading it locally instead of calling `hub.pull()`. This avoids a network request at startup.
- **Rate limiting**: wrap the `DuckDuckGoSearchRun` tool with a `RateLimiter` or add retry logic. DuckDuckGo's unofficial API rate-limits aggressively under load.
- **Session memory**: to maintain conversation context across `/invoke` calls, add `ConversationBufferMemory` keyed by `session_id`. The `@lru_cache` on `get_agent_executor()` returns a single shared executor — create one executor per session ID instead for per-user memory isolation.
- **`MOLT_ROOT` in the REPL**: `scripts/chat.py` prepends `$MOLT_ROOT/research_agent` to `sys.path`. This makes the script work both in development (where `MOLT_ROOT` is unset and falls back to `.`) and in production (where the launcher sets `MOLT_ROOT` to the install directory).
