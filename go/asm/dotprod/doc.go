// Package dotprod демонстрирует один и тот же алгоритм — скалярное
// произведение []float64 — в нескольких воплощениях: чистый Go (golden),
// рукописный AVX2 (amd64), рукописный NEON (arm64) и avo-генерация.
// Учебный стенд к серии статей «Go assembly» на khorost.tech.
package dotprod
