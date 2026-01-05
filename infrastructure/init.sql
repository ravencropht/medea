-- Создание таблицы разрешений
CREATE TABLE IF NOT EXISTS permissions (
    id SERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL UNIQUE,
    description TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Создание таблицы рабочих процессов
CREATE TABLE IF NOT EXISTS workflows (
    id SERIAL PRIMARY KEY,
    title VARCHAR(255) NOT NULL,
    status VARCHAR(50) DEFAULT 'draft',
    payload JSONB, -- Для гибкого хранения данных процесса
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Пример первичных данных (опционально)
INSERT INTO permissions (name, description) VALUES ('admin', 'Полный доступ');
INSERT INTO permissions (name, description) VALUES ('editor', 'Доступ к редактированию');