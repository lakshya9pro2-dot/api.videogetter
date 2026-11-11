package resolver

import (
	"context"
	"log"
	"time"

	"vidara-api/models"
)

// TierResolver defines the methods required for a tier to resolve movie and TV media.
type TierResolver interface {
	ResolveMovie(ctx context.Context, tmdbID int) (*models.ResolutionResult, error)
	ResolveTV(ctx context.Context, tmdbID, season, episode int) (*models.ResolutionResult, error)
}

// CentralResolver coordinates the fallback logic across Tier 1, Tier 2, Tier 3, and Tier 4.
type CentralResolver struct {
	tier1 TierResolver
	tier2 TierResolver
	tier3 TierResolver
	tier4 TierResolver
}

// NewCentralResolver creates a new resolver orchestrator supporting 3 or 4 tiers.
func NewCentralResolver(tier1, tier2, tier3 TierResolver, extraTiers ...TierResolver) *CentralResolver {
	var tier4 TierResolver
	if len(extraTiers) > 0 {
		tier4 = extraTiers[0]
	}
	return &CentralResolver{
		tier1: tier1,
		tier2: tier2,
		tier3: tier3,
		tier4: tier4,
	}
}

// ResolveMovie resolves a movie TMDB ID across Tier 1 -> Tier 2 -> Tier 3 -> Tier 4 in strict order.
func (r *CentralResolver) ResolveMovie(ctx context.Context, tmdbID int) (*models.ResolutionResult, error) {
	start := time.Now()
	log.Printf("[resolver] ==================== RESOLVE MOVIE tmdb=%d ====================", tmdbID)

	// Tier 1 - Vidara & StreamHG
	if r.tier1 != nil {
		t1Start := time.Now()
		log.Printf("[resolver] [1/4] Trying Tier 1 (Vidara / StreamHG / Streamtape) for movie %d...", tmdbID)
		res, err := r.tier1.ResolveMovie(ctx, tmdbID)
		if err != nil {
			log.Printf("[resolver] [1/4 FAIL] Tier 1 error for movie %d after %v: %v", tmdbID, time.Since(t1Start), err)
		} else if res != nil && res.Success {
			log.Printf("[resolver] [1/4 SUCCESS] Resolved movie %d via Tier 1 in %v (source: %s, url: %s)", tmdbID, time.Since(t1Start), res.Source, res.URL)
			return res, nil
		} else {
			log.Printf("[resolver] [1/4 MISS] Tier 1 returned no stream for movie %d after %v, cascading to Tier 2...", tmdbID, time.Since(t1Start))
		}
	} else {
		log.Printf("[resolver] [1/4 SKIP] Tier 1 not configured, moving to Tier 2")
	}

	// Tier 2 - Extractor (VidFast)
	if r.tier2 != nil {
		t2Start := time.Now()
		log.Printf("[resolver] [2/4] Trying Tier 2 (VidFast Extractor) for movie %d...", tmdbID)
		res, err := r.tier2.ResolveMovie(ctx, tmdbID)
		if err != nil {
			log.Printf("[resolver] [2/4 FAIL] Tier 2 error for movie %d after %v: %v", tmdbID, time.Since(t2Start), err)
		} else if res != nil && res.Success {
			log.Printf("[resolver] [2/4 SUCCESS] Resolved movie %d via Tier 2 in %v (source: %s, url: %s)", tmdbID, time.Since(t2Start), res.Source, res.URL)
			return res, nil
		} else {
			log.Printf("[resolver] [2/4 MISS] Tier 2 returned no stream for movie %d after %v, cascading to Tier 3...", tmdbID, time.Since(t2Start))
		}
	} else {
		log.Printf("[resolver] [2/4 SKIP] Tier 2 not configured, moving to Tier 3")
	}

	// Tier 3 - Vidrift & CineTV (CineTV -> Evion -> Atlas -> Vela)
	if r.tier3 != nil {
		t3Start := time.Now()
		log.Printf("[resolver] [3/4] Trying Tier 3 (CineTV & Vidrift Engine) for movie %d...", tmdbID)
		res, err := r.tier3.ResolveMovie(ctx, tmdbID)
		if err != nil {
			log.Printf("[resolver] [3/4 FAIL] Tier 3 error for movie %d after %v: %v", tmdbID, time.Since(t3Start), err)
		} else if res != nil && res.Success {
			log.Printf("[resolver] [3/4 SUCCESS] Resolved movie %d via Tier 3 in %v (source: %s, url: %s)", tmdbID, time.Since(t3Start), res.Source, res.URL)
			return res, nil
		} else {
			log.Printf("[resolver] [3/4 MISS] Tier 3 returned no stream for movie %d after %v, cascading to Tier 4...", tmdbID, time.Since(t3Start))
		}
	} else {
		log.Printf("[resolver] [3/4 SKIP] Tier 3 not configured, moving to Tier 4")
	}

	// Tier 4 - ShowBox (FilmU)
	if r.tier4 != nil {
		t4Start := time.Now()
		log.Printf("[resolver] [4/4] Trying Tier 4 (ShowBox / Febbox Scraper) for movie %d...", tmdbID)
		res, err := r.tier4.ResolveMovie(ctx, tmdbID)
		if err != nil {
			log.Printf("[resolver] [4/4 FAIL] Tier 4 error for movie %d after %v: %v", tmdbID, time.Since(t4Start), err)
		} else if res != nil && res.Success {
			log.Printf("[resolver] [4/4 SUCCESS] Resolved movie %d via Tier 4 in %v (source: %s, url: %s)", tmdbID, time.Since(t4Start), res.Source, res.URL)
			return res, nil
		} else {
			log.Printf("[resolver] [4/4 MISS] Tier 4 returned no stream for movie %d after %v", tmdbID, time.Since(t4Start))
		}
	} else {
		log.Printf("[resolver] [4/4 SKIP] Tier 4 not configured")
	}

	log.Printf("[resolver] [NOT FOUND] Movie %d not found in any tier (total time: %v)", tmdbID, time.Since(start))
	return &models.ResolutionResult{
		Success: false,
		TmdbID:  tmdbID,
		Error:   "source_not_found",
	}, nil
}

// ResolveTV resolves a TV show TMDB ID, season, and episode across Tier 1 -> Tier 2 -> Tier 3 -> Tier 4 in strict order.
func (r *CentralResolver) ResolveTV(ctx context.Context, tmdbID, season, episode int) (*models.ResolutionResult, error) {
	start := time.Now()
	log.Printf("[resolver] ==================== RESOLVE TV tmdb=%d S%02dE%02d ====================", tmdbID, season, episode)

	// Tier 1 - Vidara & StreamHG
	if r.tier1 != nil {
		t1Start := time.Now()
		log.Printf("[resolver] [1/4] Trying Tier 1 (Vidara / StreamHG / Streamtape) for TV %d S%02dE%02d...", tmdbID, season, episode)
		res, err := r.tier1.ResolveTV(ctx, tmdbID, season, episode)
		if err != nil {
			log.Printf("[resolver] [1/4 FAIL] Tier 1 error for TV %d S%02dE%02d after %v: %v", tmdbID, season, episode, time.Since(t1Start), err)
		} else if res != nil && res.Success {
			log.Printf("[resolver] [1/4 SUCCESS] Resolved TV %d S%02dE%02d via Tier 1 in %v (source: %s, url: %s)", tmdbID, season, episode, time.Since(t1Start), res.Source, res.URL)
			return res, nil
		} else {
			log.Printf("[resolver] [1/4 MISS] Tier 1 returned no stream for TV %d S%02dE%02d after %v, cascading to Tier 2...", tmdbID, season, episode, time.Since(t1Start))
		}
	} else {
		log.Printf("[resolver] [1/4 SKIP] Tier 1 not configured, moving to Tier 2")
	}

	// Tier 2 - Extractor (VidFast)
	if r.tier2 != nil {
		t2Start := time.Now()
		log.Printf("[resolver] [2/4] Trying Tier 2 (VidFast Extractor) for TV %d S%02dE%02d...", tmdbID, season, episode)
		res, err := r.tier2.ResolveTV(ctx, tmdbID, season, episode)
		if err != nil {
			log.Printf("[resolver] [2/4 FAIL] Tier 2 error for TV %d S%02dE%02d after %v: %v", tmdbID, season, episode, time.Since(t2Start), err)
		} else if res != nil && res.Success {
			log.Printf("[resolver] [2/4 SUCCESS] Resolved TV %d S%02dE%02d via Tier 2 in %v (source: %s, url: %s)", tmdbID, season, episode, time.Since(t2Start), res.Source, res.URL)
			return res, nil
		} else {
			log.Printf("[resolver] [2/4 MISS] Tier 2 returned no stream for TV %d S%02dE%02d after %v, cascading to Tier 3...", tmdbID, season, episode, time.Since(t2Start))
		}
	} else {
		log.Printf("[resolver] [2/4 SKIP] Tier 2 not configured, moving to Tier 3")
	}

	// Tier 3 - Vidrift & CineTV (CineTV -> Evion -> Atlas -> Vela)
	if r.tier3 != nil {
		t3Start := time.Now()
		log.Printf("[resolver] [3/4] Trying Tier 3 (CineTV & Vidrift Engine) for TV %d S%02dE%02d...", tmdbID, season, episode)
		res, err := r.tier3.ResolveTV(ctx, tmdbID, season, episode)
		if err != nil {
			log.Printf("[resolver] [3/4 FAIL] Tier 3 error for TV %d S%02dE%02d after %v: %v", tmdbID, season, episode, time.Since(t3Start), err)
		} else if res != nil && res.Success {
			log.Printf("[resolver] [3/4 SUCCESS] Resolved TV %d S%02dE%02d via Tier 3 in %v (source: %s, url: %s)", tmdbID, season, episode, time.Since(t3Start), res.Source, res.URL)
			return res, nil
		} else {
			log.Printf("[resolver] [3/4 MISS] Tier 3 returned no stream for TV %d S%02dE%02d after %v, cascading to Tier 4...", tmdbID, season, episode, time.Since(t3Start))
		}
	} else {
		log.Printf("[resolver] [3/4 SKIP] Tier 3 not configured, moving to Tier 4")
	}

	// Tier 4 - ShowBox (FilmU)
	if r.tier4 != nil {
		t4Start := time.Now()
		log.Printf("[resolver] [4/4] Trying Tier 4 (ShowBox / Febbox Scraper) for TV %d S%02dE%02d...", tmdbID, season, episode)
		res, err := r.tier4.ResolveTV(ctx, tmdbID, season, episode)
		if err != nil {
			log.Printf("[resolver] [4/4 FAIL] Tier 4 error for TV %d S%02dE%02d after %v: %v", tmdbID, season, episode, time.Since(t4Start), err)
		} else if res != nil && res.Success {
			log.Printf("[resolver] [4/4 SUCCESS] Resolved TV %d S%02dE%02d via Tier 4 in %v (source: %s, url: %s)", tmdbID, season, episode, time.Since(t4Start), res.Source, res.URL)
			return res, nil
		} else {
			log.Printf("[resolver] [4/4 MISS] Tier 4 returned no stream for TV %d S%02dE%02d after %v", tmdbID, season, episode, time.Since(t4Start))
		}
	} else {
		log.Printf("[resolver] [4/4 SKIP] Tier 4 not configured")
	}

	log.Printf("[resolver] [NOT FOUND] TV %d S%02dE%02d not found in any tier (total time: %v)", tmdbID, season, episode, time.Since(start))
	return &models.ResolutionResult{
		Success: false,
		TmdbID:  tmdbID,
		Season:  &season,
		Episode: &episode,
		Error:   "source_not_found",
	}, nil
}
