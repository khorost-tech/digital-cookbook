package main

import (
	"fmt"
	"hash/fnv"
	"strconv"
)

type CountMin struct {
	width, depth uint
	table        [][]uint64
	seeds        []uint32
}

func NewCountMin(width, depth uint) *CountMin {
	t := make([][]uint64, depth)
	seeds := make([]uint32, depth)
	for i := range t {
		t[i] = make([]uint64, width)
		seeds[i] = uint32(i*0x9e3779b1 + 1) // детерминированные seed'ы
	}
	return &CountMin{width: width, depth: depth, table: t, seeds: seeds}
}

func (c *CountMin) idx(key string, row uint) uint {
	h := fnv.New32a()
	h.Write([]byte(strconv.FormatUint(uint64(c.seeds[row]), 10) + key))
	return uint(h.Sum32()) % c.width
}

func (c *CountMin) Add(key string, n uint64) {
	for r := uint(0); r < c.depth; r++ {
		c.table[r][c.idx(key, r)] += n
	}
}

func (c *CountMin) Estimate(key string) uint64 {
	var min uint64 = ^uint64(0)
	for r := uint(0); r < c.depth; r++ {
		if v := c.table[r][c.idx(key, r)]; v < min {
			min = v
		}
	}
	return min
}

func runCountMin() {
	for _, dims := range [][2]uint{{500, 3}, {2000, 5}, {8000, 7}} {
		cm := NewCountMin(dims[0], dims[1])
		truth := map[string]uint64{}
		for i := 0; i < 200_000; i++ {
			k := "k-" + strconv.Itoa(i%1000)
			cm.Add(k, 1)
			truth[k]++
		}
		cm.Add("HOT", 80_000)
		truth["HOT"] += 80_000
		estHot := cm.Estimate("HOT")

		// Агрегируем переоценку по 1000 шумовым ключам — HOT её маскирует,
		// т.к. переоценка HOT (абсолютная, в единицах шума) тонет на фоне truth=80000
		// и округляется до 0.00%. Шумовые ключи (truth~200) показывают реальный тренд.
		overCount := 0
		var maxOverPct, sumOverPct float64
		for i := 0; i < 1000; i++ {
			k := "k-" + strconv.Itoa(i)
			tv := truth[k]
			est := cm.Estimate(k)
			if est > tv {
				overCount++
			}
			pct := float64(est-tv) / float64(tv) * 100
			if pct > maxOverPct {
				maxOverPct = pct
			}
			sumOverPct += pct
		}
		meanOverPct := sumOverPct / 1000

		fmt.Printf("width=%-5d depth=%d  завышено %d/1000 ключей  maxOver=%.1f%%  meanOver=%.2f%%\n",
			dims[0], dims[1], overCount, maxOverPct, meanOverPct)
		fmt.Printf("  (HOT truth=%d est=%d, никогда не занижается: est>=truth=%v)\n",
			truth["HOT"], estHot, estHot >= truth["HOT"])
	}
}
