{# Схема модели — ровно то, что задано (+schema), без префикса target-схемы. #}
{% macro generate_schema_name(custom_schema_name, node) -%}
  {{ custom_schema_name if custom_schema_name else target.schema }}
{%- endmacro %}
