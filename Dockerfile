FROM python:3.10-slim

LABEL maintainer="Office365 Manager"

# Set work directory
WORKDIR /app

# Install system dependencies for locale support
RUN apt-get update && \
    apt-get install -y --no-install-recommends locales && \
    sed -i '/zh_CN.UTF-8/s/^# //g' /etc/locale.gen && \
    locale-gen && \
    rm -rf /var/lib/apt/lists/*

# Install Python dependencies
COPY requirements.txt /app/requirements.txt
RUN pip install --no-cache-dir --upgrade pip && \
    pip install --no-cache-dir -r requirements.txt

COPY app.py sku_product_mapping.json /app/
COPY templates /app/templates
COPY static /app/static

# Set environment variables
ENV LANG=zh_CN.UTF-8 \
    LC_ALL=zh_CN.UTF-8 \
    FLASK_APP=app.py \
    PYTHONUNBUFFERED=1

# Expose the port the app runs on
EXPOSE 5000

# Start the application using Gunicorn
CMD ["gunicorn", "--workers", "1", "--access-logfile", "-", "-b", "0.0.0.0:5000", "app:app"]
