FROM python:3.14-slim

WORKDIR /app

COPY src/requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY src/homer_sync.py .
COPY src/templates/ ./templates/

CMD ["python", "homer_sync.py"]
