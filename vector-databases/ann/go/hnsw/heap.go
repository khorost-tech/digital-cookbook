package hnsw

// item — кандидат поиска: узел и расстояние до запроса.
type item struct {
	id int32
	d  float32
}

// minHeap — ближайший сверху (очередь кандидатов на раскрытие).
type minHeap []item

func (h *minHeap) push(x item) {
	*h = append(*h, x)
	a := *h
	i := len(a) - 1
	for i > 0 {
		p := (i - 1) / 2
		if a[p].d <= a[i].d {
			break
		}
		a[p], a[i] = a[i], a[p]
		i = p
	}
}

func (h *minHeap) pop() item {
	a := *h
	top := a[0]
	n := len(a) - 1
	a[0] = a[n]
	*h = a[:n]
	down(*h, 0, func(x, y float32) bool { return x < y })
	return top
}

// maxHeap — самый дальний сверху (текущие ef лучших).
type maxHeap []item

func (h *maxHeap) push(x item) {
	*h = append(*h, x)
	a := *h
	i := len(a) - 1
	for i > 0 {
		p := (i - 1) / 2
		if a[p].d >= a[i].d {
			break
		}
		a[p], a[i] = a[i], a[p]
		i = p
	}
}

func (h *maxHeap) pop() item {
	a := *h
	top := a[0]
	n := len(a) - 1
	a[0] = a[n]
	*h = a[:n]
	down(*h, 0, func(x, y float32) bool { return x > y })
	return top
}

func down(a []item, i int, better func(x, y float32) bool) {
	n := len(a)
	for {
		l := 2*i + 1
		if l >= n {
			return
		}
		b := l
		if r := l + 1; r < n && better(a[r].d, a[l].d) {
			b = r
		}
		if !better(a[b].d, a[i].d) {
			return
		}
		a[i], a[b] = a[b], a[i]
		i = b
	}
}
