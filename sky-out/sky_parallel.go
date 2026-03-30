package main

import (
	"fmt"
	"sync"
)

func sky_taskParallel(tasks any) any {
	return func() any {
		items := sky_asList(tasks)
		n := len(items)
		results := make([]any, n)
		errs := make([]any, n)
		var wg sync.WaitGroup
		wg.Add(n)
		for i, t := range items {
			go func(idx int, task any) {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						errs[idx] = fmt.Sprintf("Task panic: %v", r)
					}
				}()
				r := sky_runTask(task)
				sr := sky_asSkyResult(r)
				if sr.Tag == 1 {
					errs[idx] = sr.ErrValue
				} else {
					results[idx] = sr.OkValue
				}
			}(i, t)
		}
		wg.Wait()
		for i := 0; i < n; i++ {
			if errs[i] != nil {
				return SkyErr(errs[i])
			}
		}
		return SkyOk(results)
	}
}

func sky_parallelMap(f any) any {
	return func(items any) any {
		list := sky_asList(items)
		n := len(list)
		results := make([]any, n)
		var wg sync.WaitGroup
		wg.Add(n)
		for i, item := range list {
			go func(idx int, it any) {
				defer wg.Done()
				fn := f.(func(any) any)
				results[idx] = fn(it)
			}(i, item)
		}
		wg.Wait()
		return results
	}
}
