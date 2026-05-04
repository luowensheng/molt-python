# ML Training Pipeline — churn-model

This walkthrough builds a production-grade churn prediction pipeline using scikit-learn for modelling, Optuna for hyperparameter tuning, MLflow for experiment tracking, and joblib for model serialisation. A `serve` command wraps the trained model behind an MLflow model server. You will train, tune, evaluate, and serve the model — all via `molt run` — then ship the trained artefact and serving code as a single binary.

---

## 1. Project initialisation

```bash
molt init churn-model
cd churn-model
```

Add dependencies:

```bash
molt add scikit-learn pandas mlflow optuna joblib
molt add --dev pytest ruff
```

```
Resolving dependencies...
  + scikit-learn 1.6.0
  + pandas 2.2.3
  + mlflow 2.19.0
  + optuna 4.1.0
  + joblib 1.4.2
  + pytest 8.3.4 [dev]
  + ruff 0.8.2 [dev]
Syncing global store...
  10 packages installed  →  ~/.molt/pkg/  (198.4 MB)
Wrote .molt/syspath.json
Wrote .molt/bin/mlflow, .molt/bin/optuna, .molt/bin/ruff, .molt/bin/pytest
```

---

## 2. Project layout

```
churn-model/
├── pyproject.toml
├── uv.lock
├── molt.yaml
├── churn_model/
│   ├── __init__.py
│   ├── config.py
│   ├── features.py
│   ├── train.py
│   ├── evaluate.py
│   └── tune.py
├── scripts/
│   ├── run_train.py
│   ├── run_evaluate.py
│   ├── run_tune.py
│   └── run_serve.py
├── data/
│   ├── train.csv
│   └── test.csv
├── models/                    ← saved model artefacts land here
│   └── .gitkeep
├── mlruns/                    ← MLflow tracking directory
└── tests/
    └── test_pipeline.py
```

---

## 3. pyproject.toml

```toml
[project]
name = "churn-model"
version = "3.0.0"
requires-python = ">=3.12"
dependencies = [
  "scikit-learn>=1.6",
  "pandas>=2.2",
  "mlflow>=2.19",
  "optuna>=4.1",
  "joblib>=1.4",
]

[project.optional-dependencies]
dev = [
  "pytest>=8.3",
  "ruff>=0.8",
]

[tool.molt.tasks]
train    = "python scripts/run_train.py"
evaluate = "python scripts/run_evaluate.py"
tune     = "python scripts/run_tune.py --n-trials 100"
serve    = "mlflow models serve -m models/best-model --port 5001 --no-conda"

[tool.ruff.lint]
select = ["E", "F", "I", "UP"]
```

---

## 4. Feature engineering and training

**`churn_model/features.py`**

```python
import pandas as pd
from sklearn.pipeline import Pipeline
from sklearn.preprocessing import StandardScaler, OneHotEncoder
from sklearn.compose import ColumnTransformer
from sklearn.impute import SimpleImputer

NUMERIC_FEATURES = ["tenure", "monthly_charges", "total_charges", "num_products"]
CATEGORICAL_FEATURES = ["contract_type", "internet_service", "payment_method"]

def build_preprocessor() -> ColumnTransformer:
    numeric_transformer = Pipeline([
        ("imputer", SimpleImputer(strategy="median")),
        ("scaler", StandardScaler()),
    ])
    categorical_transformer = Pipeline([
        ("imputer", SimpleImputer(strategy="most_frequent")),
        ("encoder", OneHotEncoder(handle_unknown="ignore", sparse_output=False)),
    ])
    return ColumnTransformer([
        ("num", numeric_transformer, NUMERIC_FEATURES),
        ("cat", categorical_transformer, CATEGORICAL_FEATURES),
    ])
```

**`churn_model/train.py`**

```python
import os
import mlflow
import joblib
import pandas as pd
from sklearn.ensemble import GradientBoostingClassifier
from sklearn.pipeline import Pipeline
from sklearn.metrics import roc_auc_score
from churn_model.features import build_preprocessor, NUMERIC_FEATURES, CATEGORICAL_FEATURES
from churn_model.config import settings

def train(params: dict | None = None) -> float:
    params = params or {
        "n_estimators": 200, "max_depth": 4,
        "learning_rate": 0.05, "subsample": 0.8,
    }
    df_train = pd.read_csv(os.path.join(settings.data_dir, "train.csv"))
    X = df_train[NUMERIC_FEATURES + CATEGORICAL_FEATURES]
    y = df_train["churned"].astype(int)

    model = Pipeline([
        ("preprocessor", build_preprocessor()),
        ("classifier", GradientBoostingClassifier(**params, random_state=42)),
    ])

    with mlflow.start_run():
        mlflow.log_params(params)
        model.fit(X, y)

        df_test = pd.read_csv(os.path.join(settings.data_dir, "test.csv"))
        X_test = df_test[NUMERIC_FEATURES + CATEGORICAL_FEATURES]
        y_test = df_test["churned"].astype(int)
        auc = roc_auc_score(y_test, model.predict_proba(X_test)[:, 1])

        mlflow.log_metric("roc_auc", auc)
        mlflow.sklearn.log_model(model, "model", registered_model_name="churn-model")

        model_path = os.path.join(settings.model_dir, "best-model")
        mlflow.sklearn.save_model(model, model_path)
        print(f"ROC-AUC: {auc:.4f}  →  saved to {model_path}")
    return auc
```

**`churn_model/tune.py`**

```python
import optuna
from churn_model.train import train

def objective(trial: optuna.Trial) -> float:
    params = {
        "n_estimators": trial.suggest_int("n_estimators", 100, 500),
        "max_depth": trial.suggest_int("max_depth", 2, 8),
        "learning_rate": trial.suggest_float("learning_rate", 1e-3, 0.3, log=True),
        "subsample": trial.suggest_float("subsample", 0.5, 1.0),
        "min_samples_split": trial.suggest_int("min_samples_split", 2, 20),
    }
    return train(params)

def tune(n_trials: int = 100) -> None:
    study = optuna.create_study(direction="maximize", study_name="churn-tuning")
    study.optimize(objective, n_trials=n_trials, show_progress_bar=True)
    print(f"\nBest trial: ROC-AUC={study.best_value:.4f}")
    print(f"Best params: {study.best_params}")
```

---

## 5. Running the pipeline

Train with default parameters:

```bash
molt run train
```

```
MLflow tracking URI: mlruns/
Epoch 1/200: train_loss=0.6821
...
Epoch 200/200: train_loss=0.2314
ROC-AUC: 0.8741  →  saved to models/best-model/
```

Evaluate on the test set:

```bash
molt run evaluate
```

```
Loading model from models/best-model/
Test set evaluation (n=2,500):

  ROC-AUC:    0.8741
  PR-AUC:     0.7219
  Accuracy:   82.1%
  Precision:  0.714  (at threshold 0.50)
  Recall:     0.681
  F1:         0.697

Top 5 features by importance:
  1. tenure               0.284
  2. monthly_charges      0.201
  3. contract_type_Month  0.143
  4. total_charges        0.127
  5. internet_service_Fiber 0.098
```

Run hyperparameter tuning:

```bash
molt run tune -- --n-trials 100
```

```
[I 2025-12-04 11:30:02] A new study created in memory with name: churn-tuning
Trial 0:  ROC-AUC=0.8612
Trial 1:  ROC-AUC=0.8731
Trial 2:  ROC-AUC=0.8788
...
Trial 99: ROC-AUC=0.8901
[I 2025-12-04 11:41:17] Study statistics:  100 trials, best value=0.8901

Best trial: ROC-AUC=0.8901
Best params: {
  "n_estimators": 387, "max_depth": 5,
  "learning_rate": 0.042, "subsample": 0.73, "min_samples_split": 6
}
```

Serve the model via MLflow:

```bash
molt run serve
```

```
2025/12/04 11:45:02 INFO mlflow.models.flavor_backend_registry: Selected backend for flavor 'python_function'
2025/12/04 11:45:04 INFO waitress: Serving on http://127.0.0.1:5001
```

---

## 6. molt.yaml

```yaml
version: 1

project:
  name: churn-model
  version: 3.0.0
  python: "3.12"
  description: "Customer churn prediction — scikit-learn + MLflow"

deps:
  strategy: pyproject

include:
  - "churn_model/**/*.py"
  - "scripts/**/*.py"

exclude:
  - "**/__pycache__/"
  - "**/*.pyc"
  - "tests/"
  - "data/"
  - "mlruns/"

assets:
  files:
    - path: "models/best-model/MLmodel"
      required: true
      description: "MLflow model metadata"
    - path: "models/best-model/model.pkl"
      required: true
      description: "Serialised scikit-learn pipeline"
    - path: "models/best-model/requirements.txt"
      required: false
  max_file_size_mb: 200
  max_total_size_mb: 500

commands:
  default: serve

  serve:
    exec: [mlflow, models, serve, "-m", "models/best-model", "--port=5001", "--no-conda", "--host=0.0.0.0"]
    description: "Serve the model via MLflow REST API"

  evaluate:
    exec: [python, "scripts/run_evaluate.py", "--data-dir=/data"]
    description: "Evaluate model against a labeled CSV"

  train:
    exec: [python, "scripts/run_train.py", "--data-dir=/data"]
    description: "Retrain the model on new data"

env:
  PYTHONUNBUFFERED: "1"
  MLFLOW_TRACKING_URI: "mlruns"

integrity:
  verify_on_install: true
```

---

## 7. Building

Train the final model, then build the binary:

```bash
# Run full tuning and retrain with best params
molt run tune -- --n-trials 100
molt run train  # uses best params from tuning

molt build
```

```
Building churn-model v3.0.0 (linux/amd64)...
  Config:    ./molt.yaml
  ✓ Payload: 214.8 MB (1247 files)
      code:    4.1 MB   (source)
      assets:  12.6 MB  (model artefacts)
      deps:  198.1 MB   (sklearn + mlflow)
  ✓ Integrity manifest: churn-model-v3.0.0.manifest.json
  ✓ Created: churn-model-v3.0.0 (228.4 MB)
    root_hash: 2c8f4a1d9e7b3f05c8a2f4b1e9d7c3a0f6b4e2d8c1a9f7b5e3d1c8a6f4b2e0d8
    files:     1247   total: 214.8 MB
    build time: 9.3s
```

---

## 8. Deploy to another machine

```bash
scp churn-model-v3.0.0 churn-model-v3.0.0.manifest.json app@ml-api-01:/opt/models/
ssh app@ml-api-01
```

```bash
/opt/models/churn-model-v3.0.0 install
```

```
Installing churn-model v3.0.0...
  Verifying integrity...  ✓ root_hash match
  Extracting payload (1247 files)...
  ✓ Installed to /home/app/.molt/apps/churn-model/3.0.0/
```

```bash
/opt/models/churn-model-v3.0.0 run serve
```

```
2025/12/04 14:02:11 INFO mlflow.models.flavor_backend_registry: Selected backend for flavor 'python_function'
2025/12/04 14:02:13 INFO waitress: Serving on http://0.0.0.0:5001
```

Predict churn for a customer:

```bash
curl -sf http://ml-api-01:5001/invocations \
  -H 'Content-Type: application/json' \
  -d '{"dataframe_records": [{"tenure": 12, "monthly_charges": 79.5, "total_charges": 954.0, "num_products": 2, "contract_type": "Month-to-month", "internet_service": "Fiber optic", "payment_method": "Electronic check"}]}' \
  | python -m json.tool
```

```json
{"predictions": [0.73]}
```

---

## 9. Tips

- **Model artefact versioning**: MLflow logs each run with its own `model.pkl`. The `assets` block in `molt.yaml` points to `models/best-model/`, which is overwritten on each training run. Tag builds with `--version 3.0.$(date +%Y%m%d)` to produce a unique binary per training run.
- **Separate training and serving binaries**: the training binary (with `data/` and MLflow tracking) is large. Consider two `molt.yaml` files — `molt-serve.yaml` that ships only the serialised model and serving code, and `molt-train.yaml` for the full pipeline.
- **Retraining on the server**: include the `train` command in `molt.yaml` so the binary shipped to a production server can retrain against new data in `/data` without deploying new code.
- **MLflow tracking server**: set `MLFLOW_TRACKING_URI=http://mlflow.internal:5000` to send all experiment runs to a central server. The tracking URI is picked up from the environment at runtime.
- **Optuna in parallel**: Optuna supports multi-process parallelism via a shared storage backend (e.g. `postgresql://...`). Pass `--storage` to `run_tune.py` to distribute trials across machines.
