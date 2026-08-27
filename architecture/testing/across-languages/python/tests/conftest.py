import pathlib
import sys

# Модули стенда лежат рядом с tests/ — кладём корень в путь импорта.
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
